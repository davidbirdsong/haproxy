/*
 * include/haproxy/lb_rdvz-t.h
 * Types for rendezvous (HRW) subset hashing LB algorithm.
 *
 * Copyright (C) 2000-2009 Willy Tarreau - w@1wt.eu
 *
 * This library is free software; you can redistribute it and/or
 * modify it under the terms of the GNU Lesser General Public
 * License as published by the Free Software Foundation, version 2.1
 * exclusively.
 *
 * This library is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
 * Lesser General Public License for more details.
 *
 * You should have received a copy of the GNU Lesser General Public
 * License along with this library; if not, write to the Free Software
 * Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301  USA
 */

#ifndef _HAPROXY_LB_RDVZ_T_H
#define _HAPROXY_LB_RDVZ_T_H

#include <inttypes.h>

struct server;

/* One entry of the compact HRW (rendezvous) candidate array: one per
 * configured server. "backup"-flagged servers are rejected outright by
 * check_config_validity() for this hash-type (see proxy.c), so this is
 * always a single, uniform ranking domain - "at most Y servers, ever" holds
 * with no caveat. static_key depends only on server identity (see
 * chash_compute_server_key(), shared with hash-type consistent) and never
 * changes with health or load; weight tracks the server's configured
 * effective weight and is refreshed on status/weight changes. Neither field
 * is touched by up/down transitions, which is the invariant the whole
 * feature depends on: ranking is computed fresh per request from these
 * fields alone, and health/load are only ever applied as a filter *within*
 * the resulting top-Y, never a reason to extend past it.
 */
struct rdvz_entry {
	struct server *srv;
	uint64_t static_key;
	unsigned int weight;
};

struct lb_rdvz {
	struct rdvz_entry *tbl;	/* HRW candidate array over ALL configured servers */
	int len;		/* number of entries in tbl */
};

#endif /* _HAPROXY_LB_RDVZ_T_H */

/*
 * Local variables:
 *  c-indent-level: 8
 *  c-basic-offset: 8
 * End:
 */
