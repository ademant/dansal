package main

import "testing"

func TestExtractCN(t *testing.T) {
	cases := []struct {
		name string
		dn   string
		want string
	}{
		{"simple", "CN=alice,O=dansal", "alice"},
		{"cn not first", "O=dansal,CN=Bob Smith,OU=dev", "Bob Smith"},
		{"lowercase prefix", "o=dansal,cn=carol", "carol"},
		{"spaces around parts", "OU=dev,  CN=alice  ,O=dansal", "alice"},
		{"bare cn", "CN=alice", "alice"},
		{"first cn wins", "CN=first,CN=second", "first"},
		{"no cn", "O=dansal,OU=dev", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractCN(c.dn); got != c.want {
				t.Fatalf("extractCN(%q) = %q, want %q", c.dn, got, c.want)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8090", true},
		{"127.0.0.1", true},
		{"[::1]:8090", true},
		{"localhost:8090", false},
		{"192.168.1.5:8090", false},
		{"10.0.0.1:80", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopback(c.addr); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestSafeNextPath(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "/"},
		{"/", "/"},
		{"/users", "/users"},
		{"/users/alice@example.com/sessions", "/users/alice@example.com/sessions"},
		{"/maintenance?x=1", "/maintenance?x=1"},
		{"//evil.example.com", "/"},
		{"/\\evil", "/"},
		{"https://evil.example.com", "/"},
		{"http://evil.example.com/p", "/"},
		{"javascript:alert(1)", "/"},
		{"/a\rb", "/"},
		{"/a\tb", "/"},
		{"/a\nb", "/"},
		{"/a\\b", "/"},
	}
	for _, c := range cases {
		if got := safeNextPath(c.raw); got != c.want {
			t.Errorf("safeNextPath(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}
