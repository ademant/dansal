package netutil

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// #1428: safeDialContext must fall back to the next checked address (and
// family) instead of dialing only the first one.

type fakeConn struct{ net.Conn }

func ipAddrs(ss ...string) []net.IPAddr {
	var out []net.IPAddr
	for _, s := range ss {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out
}

func TestDialAddrsInTurnFallsBackToIPv4(t *testing.T) {
	var tried []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if strings.HasPrefix(addr, "[") {
			return nil, errors.New("connect: network is unreachable")
		}
		return fakeConn{}, nil
	}
	conn, err := DialAddrsInTurn(context.Background(), "tcp", "443", ipAddrs("2001:db8::1", "2001:db8::2", "192.0.2.1"), dial)
	if err != nil || conn == nil {
		t.Fatalf("expected a connection, got %v", err)
	}
	// Families interleave: the first v6, then the first v4 — the second v6 is never needed.
	if want := []string{"[2001:db8::1]:443", "192.0.2.1:443"}; strings.Join(tried, ",") != strings.Join(want, ",") {
		t.Errorf("dial order = %v, want %v", tried, want)
	}
}

func TestDialAddrsInTurnBlackholedFamilyTimesOut(t *testing.T) {
	old := AttemptTimeout
	AttemptTimeout = 50 * time.Millisecond
	defer func() { AttemptTimeout = old }()

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "[") {
			<-ctx.Done() // black-holed: hangs until the attempt times out
			return nil, ctx.Err()
		}
		return fakeConn{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := DialAddrsInTurn(ctx, "tcp", "443", ipAddrs("2001:db8::1", "192.0.2.1"), dial); err != nil {
		t.Fatalf("expected the IPv4 fallback to connect: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("fallback took %v — the per-attempt timeout didn't apply", d)
	}
}

func TestDialAddrsInTurnAllFailAndNetworkFilter(t *testing.T) {
	var tried []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		return nil, errors.New("refused " + addr)
	}
	_, err := DialAddrsInTurn(context.Background(), "tcp4", "80", ipAddrs("2001:db8::1", "192.0.2.1", "192.0.2.2"), dial)
	if err == nil || !strings.Contains(err.Error(), "192.0.2.2") {
		t.Fatalf("expected the last attempt's error, got %v", err)
	}
	if strings.Join(tried, ",") != "192.0.2.1:80,192.0.2.2:80" {
		t.Errorf("tcp4 must skip IPv6 addresses, tried %v", tried)
	}
	if _, err := DialAddrsInTurn(context.Background(), "tcp6", "80", ipAddrs("192.0.2.1"), dial); err == nil {
		t.Error("tcp6 with only IPv4 addresses must fail without dialing")
	}
}
