# rendezvous-subset: working branch notes

This file only exists on the `rdvz-subset-gotest` dev branch (and whatever
branches stack on top of it). It must NOT be carried into the patch series
sent to the HAProxy mailing list. Delete it, or simply don't cherry-pick it,
when building the submission branch.

## Branch stack (git-spice, remote `fork`)

```
master              <- mirrors upstream origin/master, untouched
 └─ rdvz-subset      <- the actual feature: C sources + doc/*.txt changes
     └─ rdvz-subset-gotest   <- gotest/ harness + this file, backup only
```

`rdvz-subset` is the branch that matters for upstream. `rdvz-subset-gotest`
is purely local tooling used to validate the feature during development; it
is never going to the list.

## What ships to the list (lives on `rdvz-subset`)

- `include/haproxy/backend-t.h` - new BE_LB_* flags/fields, lbprm.rdvz union
- `include/haproxy/lb_rdvz-t.h` (new) - struct rdvz_entry / struct lb_rdvz
- `include/haproxy/lb_rdvz.h` (new) - rdvz_get_server_hash() contract
- `include/haproxy/lb_chash.h` - chash_compute_server_key() un-staticed
- `src/lb_rdvz.c` (new) - the core algorithm, lb_ops registration
- `src/lb_chash.c` - same export change as the header
- `src/cfgparse-listen.c` - hash-type rendezvous-subset, hash-candidates,
  hash-subset-mode, hash-threshold, hash-decay keyword parsing
- `src/proxy.c` - sample expr compile/validate, backup-server rejection,
  cleanup ordering
- `src/backend.c` - get_server_expr()/assign_server() dispatch to RDVZ
- `src/server.c` - cli_parse_add_server() backup-server rejection
- `Makefile` - src/lb_rdvz.o
- `doc/configuration.txt` - hash-type, hash-candidates, hash-subset-mode,
  hash-threshold, hash-decay reference entries
- `doc/management.txt` - section 9.6, multi-tenant blast-radius containment

## What never ships (lives only on `rdvz-subset-gotest` or stays untracked)

- `gotest/` - the Go test harness. Not HAProxy's own reg-tests/varnishtest
  system, just local validation tooling. Exclude entirely.
- `CLAUDE.md` (this file)
- Anything untracked in the working directory that's still gitignored by
  the top-level `.gitignore` allow-list (design notes, summary docs,
  scratch scripts) - these were never committed to any branch, so there's
  nothing to strip, just don't force-add them onto a submission branch.

## Building the submission branch

1. Make sure `rdvz-subset` is rebased on a current `master`
   (`git spice branch restack` or manual rebase + `git format-patch master`
   once ready).
2. Branch off `master` fresh (not off `rdvz-subset-gotest`!) and cherry-pick
   or rebase only the `rdvz-subset` commit(s) onto it.
3. Split that single working commit into the CONTRIBUTING-mandated series:
   one patch per logical change, bug fixes (if any slip in) first, doc
   changes travel with the feature patch that introduces the keyword.
   Subject format: `SEVERITY: area: description` (new-feature patches
   typically carry no SEVERITY tag per CONTRIBUTING).
4. `git format-patch master` against that clean branch, not against
   `rdvz-subset` or `rdvz-subset-gotest` directly.

## Known open items (see conversation history / fork for detail)

- `rdvz_pick_leastconn()`'s load metric now folds in `queueslength`
  (fixed), confirmed via `gotest/rendezvous_saturation_test.go`.
- Backup servers are rejected outright (config time + `add server`), not
  mixed in - deliberate, maintainer feedback pending.
- Dynamic `add server`/`del server` supported via `BE_LB_PROP_DYN`.
