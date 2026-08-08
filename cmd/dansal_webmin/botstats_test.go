package main

import (
	"encoding/json"
	"testing"
)

type chartPayload struct {
	Dates      []string         `json:"dates"`
	Categories []botCat         `json:"categories"`
	Series     map[string][]int `json:"series"`
}

func decodeChart(t *testing.T, raw string) chartPayload {
	t.Helper()
	var m chartPayload
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("invalid chart JSON: %v", err)
	}
	return m
}

func TestChartDates(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"2026-08-01", "2026-08-31"}, []string{"08-01", "08-31"}},
		{[]string{"2026-08-01", "short"}, []string{"08-01", ""}},
		{[]string{}, []string{}},
	}
	for _, c := range cases {
		got := chartDates(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("chartDates(%v) len = %d, want %d", c.in, len(got), len(c.want))
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("chartDates(%v) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestBuildChartJSON(t *testing.T) {
	got := decodeChart(t, buildChartJSON(
		[]string{"2026-08-01", "2026-08-02"},
		[]botCat{{"a", "A", "#fff"}},
		map[string][]int{"a": {1, 2}},
	))
	if len(got.Dates) != 2 || got.Dates[0] != "08-01" || got.Dates[1] != "08-02" {
		t.Fatalf("dates = %v, want [08-01 08-02]", got.Dates)
	}
	if len(got.Categories) != 1 || got.Categories[0].Key != "a" {
		t.Fatalf("categories = %v, want [{a A #fff}]", got.Categories)
	}
	if len(got.Series["a"]) != 2 || got.Series["a"][0] != 1 || got.Series["a"][1] != 2 {
		t.Fatalf("series[a] = %v, want [1 2]", got.Series["a"])
	}
}

func TestBuildBotChartJSON(t *testing.T) {
	days := []botStatDay{
		{Date: "2026-08-01", Categories: map[string]int{"browser": 3, "ai_crawler": 1}},
		{Date: "2026-08-02", Categories: map[string]int{"browser": 5}},
	}
	got := decodeChart(t, buildBotChartJSON(days))
	if len(got.Dates) != 2 || got.Dates[0] != "08-01" {
		t.Fatalf("dates = %v, want truncated [08-01 08-02]", got.Dates)
	}
	if len(got.Series) != len(botCategoryList) {
		t.Fatalf("series has %d keys, want %d (one per category)", len(got.Series), len(botCategoryList))
	}
	for _, c := range botCategoryList {
		if len(got.Series[c.Key]) != 2 {
			t.Fatalf("series[%s] len = %d, want 2", c.Key, len(got.Series[c.Key]))
		}
	}
	if got.Series["browser"][0] != 3 || got.Series["browser"][1] != 5 {
		t.Fatalf("series[browser] = %v, want [3 5]", got.Series["browser"])
	}
	if got.Series["ai_crawler"][0] != 1 || got.Series["ai_crawler"][1] != 0 {
		t.Fatalf("series[ai_crawler] = %v, want [1 0]", got.Series["ai_crawler"])
	}
}

func TestBuildUserChartJSON(t *testing.T) {
	days := []userStatDay{
		{Date: "2026-08-01", DirectCount: 10, SearchCount: 4, ExternalCount: 2, InternalCount: 1},
		{Date: "2026-08-02", DirectCount: 5},
	}
	got := decodeChart(t, buildUserChartJSON(days))
	if got.Series["direct"][0] != 10 || got.Series["direct"][1] != 5 {
		t.Fatalf("series[direct] = %v, want [10 5]", got.Series["direct"])
	}
	if got.Series["search"][0] != 4 || got.Series["search"][1] != 0 {
		t.Fatalf("series[search] = %v, want [4 0]", got.Series["search"])
	}
	if got.Series["external"][0] != 2 || got.Series["internal"][0] != 1 {
		t.Fatalf("series external/internal = %v", got.Series)
	}
}

func TestBuildFediChartJSON(t *testing.T) {
	days := []fediStatDay{
		{Date: "2026-08-01", Inbox2xx: 100, Inbox4xx: 5, Inbox5xx: 1, ActorFetches: 20, WebfingerRequests: 3},
		{Date: "2026-08-02", Inbox2xx: 50},
	}
	got := decodeChart(t, buildFediChartJSON(days))
	if len(got.Dates) != 2 || got.Dates[0] != "08-01" {
		t.Fatalf("dates = %v, want truncated", got.Dates)
	}
	checks := map[string][]int{
		"inbox_2xx":     {100, 50},
		"inbox_4xx":     {5, 0},
		"inbox_5xx":     {1, 0},
		"actor_fetches": {20, 0},
		"webfinger":     {3, 0},
	}
	for key, want := range checks {
		got := got.Series[key]
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("series[%s] = %v, want %v", key, got, want)
		}
	}
}

func TestBuildFediFollowChartJSON(t *testing.T) {
	days := []fediStatDay{
		{Date: "2026-08-01", FollowersGained: 7, EventsDeliveredCreates: 30, EventsDeliveredUpdates: 12},
		{Date: "2026-08-02", FollowersGained: 2, EventsDeliveredCreates: 1, EventsDeliveredUpdates: 0},
	}
	got := decodeChart(t, buildFediFollowChartJSON(days))
	if got.Series["gained"][0] != 7 || got.Series["gained"][1] != 2 {
		t.Fatalf("series[gained] = %v, want [7 2]", got.Series["gained"])
	}
	if got.Series["delivers"][0] != 42 || got.Series["delivers"][1] != 1 {
		t.Fatalf("series[delivers] = %v, want [42 1]", got.Series["delivers"])
	}
}
