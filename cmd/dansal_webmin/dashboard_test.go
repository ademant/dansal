package main

import (
	"strings"
	"testing"
)

func TestFmtBytes(t *testing.T) {
	cases := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1572864, "1.5 MiB"},
		{1073741824, "1.0 GiB"},
		{2147483648, "2.0 GiB"},
	}
	for _, c := range cases {
		if got := fmtBytes(c.b); got != c.want {
			t.Errorf("fmtBytes(%d) = %q, want %q", c.b, got, c.want)
		}
	}
}

func TestMonitoredUnits(t *testing.T) {
	def := monitoredUnits("")
	if len(def) != 9 {
		t.Fatalf("default units = %d, want 9 (%v)", len(def), def)
	}
	if def[0] != "dansal" {
		t.Fatalf("default units[0] = %q, want dansal", def[0])
	}
	found := false
	for _, u := range def {
		if u == "dansal-fetch.timer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("default units missing dansal-fetch.timer: %v", def)
	}

	inst := monitoredUnits("dev")
	if len(inst) != 9 || inst[0] != "dansal@dev" {
		t.Fatalf("instance units = %v, want dansal@dev first", inst)
	}
	for _, u := range inst {
		if !strings.HasSuffix(u, "@dev") && !strings.HasSuffix(u, "@dev.timer") {
			t.Fatalf("unit %q missing @dev suffix", u)
		}
	}
}

func TestServiceStatusBadge(t *testing.T) {
	cases := []struct {
		active string
		want   string
	}{
		{"active", "ok"},
		{"failed", "danger"},
		{"inactive", "warn"},
		{"activating", "warn"},
		{"", "warn"},
	}
	for _, c := range cases {
		s := ServiceStatus{Name: "x", ActiveState: c.active}
		if got := s.Badge(); got != c.want {
			t.Errorf("ServiceStatus{ActiveState:%q}.Badge() = %q, want %q", c.active, got, c.want)
		}
	}
}
