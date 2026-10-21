/*
 * Rendezvous (HRW) subset hashing LB algorithm.
 *
 * Unlike consistent hashing, this algorithm never shrinks a tenant's
 * candidate set when servers go down: the top-Y ranking for a given hash
 * key is computed fresh from static, health-independent server identity on
 * every request, and health/load are only ever used to filter *inside*
 * that already-determined Y-sized subset. This is the central invariant
 * the whole feature depends on; see rdvz_get_server_hash() below.
 *
 * Copyright 2000-2010 Willy Tarreau <w@1wt.eu>
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version
 * 2 of the License, or (at your option) any later version.
 *
 */

#include <math.h>

#include <haproxy/api.h>
#include <haproxy/backend.h>
#include <haproxy/lb_chash.h>
#include <haproxy/lb_rdvz.h>
#include <haproxy/proxy.h>
#include <haproxy/queue.h>
#include <haproxy/server.h>
#include <haproxy/tools.h>
#include <haproxy/xxhash.h>

/* Hard cap on the number of candidates considered per request, independent
 * of the configured "hash-candidates" value (which may come from a
 * per-request sample expression). This bounds the per-thread scratch
 * buffer below and the per-request CPU cost; it is not part of the
 * documented config contract, just a defensive backstop.
 */
#define RDVZ_MAX_Y 1024

struct rdvz_cand {
  struct rdvz_entry *e;
  double score;
};

static THREAD_LOCAL struct rdvz_cand rdvz_top[RDVZ_MAX_Y];

/* (Re)builds the candidate table from proxy <p>'s current server list.
 * check_config_validity() rejects any "backup"-flagged server on a
 * rendezvous-subset backend (see proxy.c), so this always ranks a single,
 * uniform pool - no active/backup split to reason about.
 *
 * Only safe to call where every server currently linked in p->servers is
 * itself fully valid to dereference - true at config time, on a status/
 * weight change, and when a new dynamic server is being added (it's
 * already linked by the time ops->server_init() runs). NOT used for
 * removal (see rdvz_server_deinit(): during whole-proxy teardown,
 * sibling servers elsewhere in p->servers may themselves be mid-teardown,
 * so walking the list there is unsafe - removal instead does a targeted
 * purge of the existing table, below).
 *
 * Must be called with the lbprm lock held for writing. Builds the
 * replacement array fully off to the side and only then swaps it in and
 * frees the old one, so a concurrent reader (holding only the read lock)
 * always sees either the complete old table or the complete new one,
 * never a partial one. Returns 0 on success, -1 on allocation failure
 * (table left unchanged).
 */
static int rdvz_build_table(struct proxy *p) {
  struct server *srv;
  struct rdvz_entry *tbl, *old;
  int n = 0;

  list_for_each_entry(srv, &p->servers, el_px) n++;

  tbl = n ? calloc(n, sizeof(*tbl)) : NULL;
  if (n && !tbl)
    return -1;

  n = 0;
  list_for_each_entry(srv, &p->servers, el_px) {
    tbl[n].srv = srv;
    tbl[n].static_key = chash_compute_server_key(srv);
    tbl[n].weight = srv->next_eweight;
    n++;
  }

  old = p->lbprm.rdvz.tbl;
  p->lbprm.rdvz.tbl = tbl;
  p->lbprm.rdvz.len = n;
  free(old);
  return 0;
}

/* Removes <srv>'s entry (if present) from <p>'s existing candidate table,
 * without touching p->servers or any other server struct at all - unlike
 * rdvz_build_table(), which re-derives everything from the server list and
 * is therefore unsafe to call from rdvz_server_deinit() (see its comment).
 * This only ever reads the table rdvz_build_table() already built, which by
 * construction contains nothing but valid, still-allocated entries. Must be
 * called with the lbprm lock held for writing.
 */
static void rdvz_remove_from_table(struct proxy *p, const struct server *srv) {
  struct rdvz_entry *tbl, *old = p->lbprm.rdvz.tbl;
  int old_len = p->lbprm.rdvz.len;
  int n = 0, i;

  /* Same size as before: at most one fewer live entry than allocated,
   * which is harmless, and keeps this allocation infallible-in-practice
   * (same size that was already live a moment ago) without needing an
   * OOM fallback path here.
   */
  tbl = old_len ? calloc(old_len, sizeof(*tbl)) : NULL;
  if (old_len && !tbl)
    return; /* OOM: leave the table as-is; srv's entry going stale
             * until the next successful rebuild is no worse than
             * the risk this function exists to avoid. */

  for (i = 0; i < old_len; i++) {
    if (old[i].srv != srv)
      tbl[n++] = old[i];
  }

  p->lbprm.rdvz.tbl = tbl;
  p->lbprm.rdvz.len = n;
  free(old);
}

/* This function is responsible for building the initial candidate table at
 * config time. Returns 0 on success, -1 on allocation failure.
 */
static int rdvz_init_server_table(struct proxy *p) {
  struct server *srv;

  p->lbprm.wdiv = BE_WEIGHT_SCALE;
  list_for_each_entry(srv, &p->servers, el_px) {
    srv->next_eweight =
        (srv->uweight * p->lbprm.wdiv + p->lbprm.wmult - 1) / p->lbprm.wmult;
    srv_lb_commit_status(srv);
  }

  recount_servers(p);
  update_backend_weight(p);
  return rdvz_build_table(p);
}

/* Rebuilds the candidate table after a status or weight change.
 * srv_lb_status_changed() covers both: it compares next_state/next_admin/
 * next_eweight against the last commit, so a weight-only change (no
 * usability change) still triggers a rebuild.
 *
 * The server's lock must be held. The lbprm lock will be used.
 */
static void rdvz_update_servers(struct server *srv) {
  struct proxy *p = srv->proxy;

  if (!srv_lb_status_changed(srv))
    return;

  HA_RWLOCK_WRLOCK(LBPRM_LOCK, &p->lbprm.lock);
  recount_servers(p);
  update_backend_weight(p);
  rdvz_build_table(p);
  HA_RWLOCK_WRUNLOCK(LBPRM_LOCK, &p->lbprm.lock);

  srv_lb_commit_status(srv);
}

/* ops->server_init: called once for a server added dynamically at runtime
 * via the "add server" CLI command (srv_alloc_lb() in src/server.c), never
 * for config-time servers (those are covered by rdvz_init_server_table()
 * instead - srv_alloc_lb() is only reached from the CLI add-server path).
 * The new server is already linked into p->servers by this point, so a
 * plain rebuild (no exclusion) picks it up correctly. Neither
 * thread_isolate() (ambient during CLI add) nor anything else holds the
 * lbprm lock for us here, so we take it ourselves, matching
 * rdvz_update_servers(). Always succeeds other than on OOM.
 */
static int rdvz_server_init(struct server *srv) {
  struct proxy *p = srv->proxy;
  int ret;

  HA_RWLOCK_WRLOCK(LBPRM_LOCK, &p->lbprm.lock);
  ret = rdvz_build_table(p);
  HA_RWLOCK_WRUNLOCK(LBPRM_LOCK, &p->lbprm.lock);
  return ret;
}

/* ops->server_deinit: called once for every server being torn down, in two
 * distinct situations that both land here:
 *
 *  - runtime "del server": strictly before srv is unlinked from p->servers
 *    (srv_detach()) and strictly before the struct is ever freed (srv_drop(),
 *    which only runs after the CLI handler's thread_isolate_full() section -
 *    which this call is part of - has already ended). "del server" itself
 *    refuses to run unless the server is already in maintenance, so by this
 *    point rdvz_update_servers() has already rebuilt the table reflecting it
 *    as down; this call's job is specifically to drop its entry from the
 *    table entirely (not just leave it flagged down), since the pointer is
 *    about to become invalid.
 *
 *  - whole-proxy teardown (deinit_proxy()): called once per server, in a
 *    plain loop over p->servers, interleaved with srv_drop() calls that may
 *    free earlier-processed servers in that same loop before later ones are
 *    reached. This is why this function purges the one dying entry via
 *    rdvz_remove_from_table() (which only ever reads the already-built
 *    table) rather than via rdvz_build_table() (which would re-walk
 *    p->servers and risk dereferencing one of those already-freed siblings).
 *
 * Takes the lbprm lock itself, same reasoning as rdvz_server_init(): nothing
 * above us holds it in either calling context.
 */
static void rdvz_server_deinit(struct server *srv) {
  struct proxy *p = srv->proxy;

  HA_RWLOCK_WRLOCK(LBPRM_LOCK, &p->lbprm.lock);
  rdvz_remove_from_table(p, srv);
  HA_RWLOCK_WRUNLOCK(LBPRM_LOCK, &p->lbprm.lock);
}

/* u in (0,1), derived deterministically from a server's static key and the
 * request hash. Never exactly 0 (log(0) is undefined) or 1.
 */
static inline double rdvz_unit(uint64_t static_key, unsigned int hash) {
  uint64_t combined = XXH3(&hash, sizeof(hash), static_key) | 1;

  return (double)combined * (1.0 / 18446744073709551616.0 /* 2^64 */);
}

/* Weighted HRW score for entry <e> against request <hash>. Depends only on
 * the server's static identity key and its configured weight, never on
 * current health or load: this is what makes the resulting ranking
 * reproducible and health-independent for a given <hash>. A non-positive
 * weight (drained server) scores below every real candidate, but is not
 * excluded from the table itself (see rdvz_build_table()).
 */
static inline double rdvz_score(const struct rdvz_entry *e, unsigned int hash) {
  if (e->weight == 0)
    return -1.0;
  return (double)e->weight / -log(rdvz_unit(e->static_key, hash));
}

/* leastconn: weighted argmin over the Y candidates (excluding <avoid>).
 * Prefers an unsaturated candidate; if every usable candidate is saturated,
 * falls back to the argmin among usable candidates anyway so the caller's
 * existing per-server admission path (assign_server_and_queue) handles
 * queueing - "momentarily full" is the only state this fallback covers.
 * A down/drained candidate is never eligible, not even as that fallback.
 * Returns NULL if <avoid> excludes every candidate, or if none of the
 * <top_n> candidates are currently usable at all.
 *
 * The load metric includes queueslength alongside served: served alone
 * plateaus at maxconn, so once two or more candidates are simultaneously
 * saturated it can no longer tell them apart, and ties resolve to whichever
 * was encountered first (i.e. the highest-ranked one) regardless of how
 * much deeper its queue already is than a sibling's. Folding queueslength
 * in keeps the metric monotonic past that plateau, so the saturated
 * fallback still prefers whichever candidate is least backed up.
 */
static struct server *rdvz_pick_leastconn(struct rdvz_cand *top, int top_n,
                                          const struct server *avoid) {
  struct server *best_ok = NULL, *best_any = NULL;
  double best_ok_load = 0, best_any_load = 0;
  int i;

  for (i = 0; i < top_n; i++) {
    struct server *s = top[i].e->srv;
    double w, load;

    if (s == avoid)
      continue;
    if (top[i].e->weight == 0 || !srv_currently_usable(s))
      continue; /* drained or down: never eligible at all, not even as fallback
                 */

    w = top[i].e->weight > 0 ? (double)top[i].e->weight : 1.0;
    load = ((double)s->served + (double)s->queueslength) / w;

    if (!best_any || load < best_any_load) {
      best_any = s;
      best_any_load = load;
    }

    if (s->maxconn && (s->queueslength || s->served >= srv_dynamic_maxconn(s)))
      continue; /* saturated: not the "ok" pick, but still the fallback
                   (best_any) */

    if (!best_ok || load < best_ok_load) {
      best_ok = s;
      best_ok_load = load;
    }
  }

  return best_ok ? best_ok : best_any;
}

/* priority: walk ranks 1..top_n (top[0] = highest HRW score for this hash).
 * At each rank, roll against a threshold that decays with rank; accept the
 * first usable, unsaturated, non-avoided candidate whose roll succeeds. If
 * none is accepted, fall back to the best-ranked usable-but-saturated
 * candidate regardless of load (the "queue at rank 1" guarantee covers
 * being "momentarily full", not being down). A down/drained candidate is
 * never eligible, not even as that fallback. Returns NULL if <avoid>
 * excludes every candidate, or if none of the <top_n> candidates are
 * currently usable at all.
 */
static struct server *rdvz_pick_priority(struct proxy *p, struct rdvz_cand *top,
                                         int top_n,
                                         const struct server *avoid) {
  struct server *fallback = NULL;
  double threshold = p->lbprm.hash_threshold;
  double rate = p->lbprm.hash_decay_rate;
  int i;

  for (i = 0; i < top_n; i++) {
    struct server *s = top[i].e->srv;
    double t, roll;

    if (s == avoid)
      continue;
    if (top[i].e->weight == 0 || !srv_currently_usable(s))
      continue; /* drained or down: never eligible at all, not even as fallback
                 */

    if (!fallback)
      fallback = s;

    if (s->maxconn && (s->queueslength || s->served >= srv_dynamic_maxconn(s)))
      continue; /* saturated: never accepted by the roll, only eligible as
                   fallback */

    if (p->lbprm.hash_decay_kind == BE_LB_HDECAY_HARMONIC)
      t = threshold / (1.0 + rate * i);
    else
      t = threshold * pow(rate, i);

    roll = (double)statistical_prng_range(1000000) /
           10000.0; /* uniform in [0, 100) */
    if (roll < t)
      return s;
  }

  return fallback;
}

/* Selects a server for proxy <p> and hash key <hash>, drawing only from the
 * top-<y> health-independent candidate subset determined by <hash>. See
 * include/haproxy/lb_rdvz.h for the full contract.
 *
 * The lbprm's lock will be used in R/O mode. The server's lock is not used.
 */
struct server *rdvz_get_server_hash(struct proxy *p, unsigned int hash, int y,
                                    const struct server *avoid) {
  struct rdvz_entry *tbl;
  struct server *best = NULL;
  int n, top_n, i;

  HA_RWLOCK_RDLOCK(LBPRM_LOCK, &p->lbprm.lock);

  n = p->lbprm.rdvz.len;
  if (!n || y <= 0)
    goto out;

  if (y > n)
    y = n;
  if (y > RDVZ_MAX_Y)
    y = RDVZ_MAX_Y;

  tbl = p->lbprm.rdvz.tbl;

  /* Build the top-y set by score, descending, via a bounded insertion.
   * N is expected to stay in the hundreds and Y small, so this O(N*Y)
   * walk beats a full O(N log N) sort of every entry (see design/PLAN.md
   * step 5: no ranking cache, recompute fresh every request).
   */
  top_n = 0;
  for (i = 0; i < n; i++) {
    double score = rdvz_score(&tbl[i], hash);
    int pos;

    if (top_n == y && score <= rdvz_top[top_n - 1].score)
      continue;

    pos = (top_n < y) ? top_n : top_n - 1;
    while (pos > 0 && rdvz_top[pos - 1].score < score) {
      rdvz_top[pos] = rdvz_top[pos - 1];
      pos--;
    }
    rdvz_top[pos].e = &tbl[i];
    rdvz_top[pos].score = score;
    if (top_n < y)
      top_n++;
  }

  if (p->lbprm.hash_subset_mode == BE_LB_HSM_LEASTCONN)
    best = rdvz_pick_leastconn(rdvz_top, top_n, avoid);
  else
    best = rdvz_pick_priority(p, rdvz_top, top_n, avoid);

out:
  HA_RWLOCK_RDUNLOCK(LBPRM_LOCK, &p->lbprm.lock);
  return best;
}

static struct lb_ops lb_rdvz_ops = {
    ILH,
    .map = {{.mask = BE_LB_KIND | BE_LB_HASH_TYPE,
             .match = BE_LB_KIND_HI | BE_LB_HASH_RDVZ},
            {0, 0}},
    .algo_prop = BE_LB_LKUP_RDVZ | BE_LB_PROP_DYN,
    .proxy_init = rdvz_init_server_table,
    .set_server_status_up = rdvz_update_servers,
    .set_server_status_down = rdvz_update_servers,
    .server_init = rdvz_server_init,
    .server_deinit = rdvz_server_deinit,
};

INITCALL1(STG_REGISTER, lb_ops_register, &lb_rdvz_ops);

/*
 * Local variables:
 *  c-indent-level: 8
 *  c-basic-offset: 8
 * End:
 */
