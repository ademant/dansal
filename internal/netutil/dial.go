// Package netutil holds network helpers shared by the dansal binaries.
package netutil

import (
	"context"
	"fmt"
	"net"
	"time"
)

// AttemptTimeout bounds every dial attempt except the last, so an
// address family that is black-holed (packets silently dropped) can't eat
// the whole request deadline before the next address is tried.
var AttemptTimeout = 4 * time.Second

// DialAddrsInTurn dials ips one after another until one connects (#1428; shared with dansal-web's federation dialer since #1432).
// Dialing a literal IP loses net.Dialer's own fallback between addresses and
// families, and resolvers usually list IPv6 first — so on a host with a broken
// IPv6 route, dialing only ips[0] failed although IPv4 would have worked.
// Families are interleaved (v6, v4, v6, …) as in Happy Eyeballs (RFC 8305),
// addresses that don't fit a tcp4/tcp6 network are skipped, and the last
// error is returned when none connects.
func DialAddrsInTurn(ctx context.Context, network, port string, ips []net.IPAddr,
	dial func(ctx context.Context, network, addr string) (net.Conn, error)) (net.Conn, error) {
	var v4, v6 []net.IPAddr
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			if network != "tcp6" {
				v4 = append(v4, ip)
			}
		} else if network != "tcp4" {
			v6 = append(v6, ip)
		}
	}
	// Start with the family the resolver listed first.
	first, second := v6, v4
	if len(ips) > 0 && ips[0].IP.To4() != nil {
		first, second = v4, v6
	}
	var ordered []net.IPAddr
	for i := 0; i < len(first) || i < len(second); i++ {
		if i < len(first) {
			ordered = append(ordered, first[i])
		}
		if i < len(second) {
			ordered = append(ordered, second[i])
		}
	}
	if len(ordered) == 0 {
		return nil, fmt.Errorf("no %s address for host", network)
	}

	var lastErr error
	for i, ip := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if i < len(ordered)-1 {
			attemptCtx, cancel = context.WithTimeout(ctx, AttemptTimeout)
		}
		conn, err := dial(attemptCtx, network, net.JoinHostPort(ip.IP.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
