package main

import (
	"net/http/httptest"
	"testing"
)

func TestTmplDataNavNormalization(t *testing.T) {
	cfg := &Config{SiteName: "test"}
	cases := []struct {
		path string
		want string
	}{
		{"/", "/"},
		{"/users", "/users"},
		{"/users/alice@example.com/sessions", "/users"},
		{"/users/bob/sessions/42", "/users"},
		{"/maintenance", "/maintenance"},
		{"/notifications", "/notifications"},
		{"/site-config", "/site-config"},
		{"/bot-stats", "/bot-stats"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", c.path, nil)
			td := tmplData(r, cfg, "T", nil)
			if td.NavPath != c.want {
				t.Fatalf("tmplData(%q).NavPath = %q, want %q", c.path, td.NavPath, c.want)
			}
		})
	}
}
