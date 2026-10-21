/*
 * include/haproxy/lb_rdvz.h
 * Function declarations for rendezvous (HRW) subset hashing LB algorithm.
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

#ifndef _HAPROXY_LB_RDVZ_H
#define _HAPROXY_LB_RDVZ_H

#include <haproxy/api.h>
#include <haproxy/backend-t.h>
#include <haproxy/lb_rdvz-t.h>

struct proxy;
struct server;

/* Selects a server for proxy <p> and hash key <hash>, drawing only from the
 * top-<y> health-independent candidate subset determined by <hash> (HRW
 * ranking over all configured servers). <y> must already be resolved (and
 * is clamped internally: <y> <= 0 is treated as "fail the request", <y> > N
 * is clamped to N). <avoid> is excluded for this attempt only.
 *
 * Only returns NULL when no server at all is configured for <p>, when <y>
 * resolves to <= 0, when <avoid> excludes every one of the Y candidates, or
 * when none of the Y candidates are currently usable (administratively
 * down, health-check down, or drained). Never returns NULL merely because
 * the subset is momentarily saturated (maxconn/queue full) - that case
 * still hands back a real server so the caller's existing per-server
 * admission path queues it normally.
 */
struct server *rdvz_get_server_hash(struct proxy *p, unsigned int hash, int y, const struct server *avoid);

#endif /* _HAPROXY_LB_RDVZ_H */

/*
 * Local variables:
 *  c-indent-level: 8
 *  c-basic-offset: 8
 * End:
 */
