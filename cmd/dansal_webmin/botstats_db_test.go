package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// writeBotStatsDB creates a temporary sqlite database and runs the given DDL/DML.
func writeBotStatsDB(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bot-stats.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return path
}

func TestLoadBotStatsDB(t *testing.T) {
	path := writeBotStatsDB(t,
		`CREATE TABLE bot_stats_meta(date TEXT, total_requests INTEGER, human_count INTEGER, bot_count INTEGER, inbox_failures INTEGER)`,
		`CREATE TABLE bot_stats_daily(date TEXT, category TEXT, count INTEGER)`,
		`CREATE TABLE user_stats_meta(date TEXT, total_requests INTEGER, direct_count INTEGER, search_count INTEGER, external_count INTEGER, internal_count INTEGER, clicks_per_entry REAL, scanner_slipthrough INTEGER)`,
		`INSERT INTO bot_stats_meta VALUES('2026-08-01', 100, 60, 40, 2)`,
		`INSERT INTO bot_stats_meta VALUES('2026-08-02', 200, 120, 80, 0)`,
		`INSERT INTO bot_stats_daily VALUES('2026-08-01','browser',50)`,
		`INSERT INTO bot_stats_daily VALUES('2026-08-01','ai_crawler',10)`,
		`INSERT INTO user_stats_meta VALUES('2026-08-01', 90, 30, 20, 10, 5, 2.5, 1)`,
	)
	botDays, userDays, engines, err := loadBotStatsDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(botDays) != 2 || botDays[0].Date != "2026-08-01" || botDays[1].Date != "2026-08-02" {
		t.Fatalf("bot days = %+v, want two days oldest-first", botDays)
	}
	if botDays[0].TotalRequests != 100 || botDays[0].Categories["browser"] != 50 || botDays[0].Categories["ai_crawler"] != 10 {
		t.Fatalf("day 1 = %+v, want totals + merged categories", botDays[0])
	}
	if botDays[1].Categories["browser"] != 0 {
		t.Fatalf("day 2 missing category should default to 0, got %+v", botDays[1])
	}
	if len(userDays) != 1 || userDays[0].DirectCount != 30 || userDays[0].ClicksPerEntry != 2.5 {
		t.Fatalf("user days = %+v, want one row with direct=30 clicks=2.5", userDays)
	}
	// user_stats_referrers table not created in this fixture → engines should be nil (graceful).
	if engines != nil {
		t.Fatalf("engines = %+v, want nil when table is absent", engines)
	}
}

func TestLoadBotStatsDBSearchEngines(t *testing.T) {
	path := writeBotStatsDB(t,
		`CREATE TABLE bot_stats_meta(date TEXT, total_requests INTEGER, human_count INTEGER, bot_count INTEGER, inbox_failures INTEGER)`,
		`CREATE TABLE bot_stats_daily(date TEXT, category TEXT, count INTEGER)`,
		`CREATE TABLE user_stats_meta(date TEXT, total_requests INTEGER, direct_count INTEGER, search_count INTEGER, external_count INTEGER, internal_count INTEGER, clicks_per_entry REAL, scanner_slipthrough INTEGER)`,
		`CREATE TABLE user_stats_referrers(date TEXT, source TEXT, count INTEGER, PRIMARY KEY(date,source))`,
		`INSERT INTO user_stats_referrers VALUES('2026-08-01','search:Google',614)`,
		`INSERT INTO user_stats_referrers VALUES('2026-08-01','search:DuckDuckGo',71)`,
		`INSERT INTO user_stats_referrers VALUES('2026-08-01','search:Bing',55)`,
		`INSERT INTO user_stats_referrers VALUES('2026-08-01','direct',120)`,
	)
	_, _, engines, err := loadBotStatsDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(engines) != 3 {
		t.Fatalf("engines = %+v, want 3 search: entries (direct excluded)", engines)
	}
	if engines[0].Engine != "Google" || engines[0].Count != 614 {
		t.Fatalf("engines[0] = %+v, want Google/614", engines[0])
	}
	if engines[1].Engine != "DuckDuckGo" || engines[1].Count != 71 {
		t.Fatalf("engines[1] = %+v, want DuckDuckGo/71", engines[1])
	}
	if engines[2].Engine != "Bing" || engines[2].Count != 55 {
		t.Fatalf("engines[2] = %+v, want Bing/55", engines[2])
	}
}

func TestLoadFediStatsDB(t *testing.T) {
	path := writeBotStatsDB(t,
		`CREATE TABLE fedi_stats_daily(date TEXT, followers_gained INTEGER, events_delivered_creates INTEGER, events_delivered_updates INTEGER, inbox_total INTEGER, inbox_2xx INTEGER, inbox_4xx INTEGER, inbox_5xx INTEGER, actor_fetches INTEGER, webfinger_requests INTEGER, nodeinfo_requests INTEGER, outbox_fetches INTEGER, followers_fetches INTEGER, event_fetches INTEGER)`,
		`CREATE TABLE fedi_source_instances(date TEXT, instance TEXT, count INTEGER)`,
		`INSERT INTO fedi_stats_daily VALUES('2026-08-01',1,10,2,50,45,4,1,20,3,0,5,6,7)`,
		`INSERT INTO fedi_stats_daily VALUES('2026-08-02',2,0,0,10,10,0,0,1,0,0,0,0,0)`,
		`INSERT INTO fedi_source_instances VALUES('2026-08-01','mastodon.social',15)`,
		`INSERT INTO fedi_source_instances VALUES('2026-08-02','mastodon.social',3)`,
		`INSERT INTO fedi_source_instances VALUES('2026-08-01','instance.test',2)`,
	)
	days, instances, err := loadFediStatsDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Date != "2026-08-01" || days[1].Date != "2026-08-02" {
		t.Fatalf("days = %+v, want two rows oldest-first", days)
	}
	if days[0].Inbox2xx != 45 || days[0].ActorFetches != 20 {
		t.Fatalf("day 0 = %+v, want inbox2xx=45 actor_fetches=20", days[0])
	}
	if len(instances) != 2 {
		t.Fatalf("instances = %+v, want 2", instances)
	}
	if instances[0].Instance != "mastodon.social" || instances[0].Count != 18 {
		t.Fatalf("instances[0] = %+v, want mastodon.social=18 (aggregated, desc)", instances[0])
	}
	if instances[1].Instance != "instance.test" || instances[1].Count != 2 {
		t.Fatalf("instances[1] = %+v, want instance.test=2", instances[1])
	}
}

func TestLoadFediStatsDBMissingTables(t *testing.T) {
	path := writeBotStatsDB(t,
		`CREATE TABLE bot_stats_meta(date TEXT, total_requests INTEGER, human_count INTEGER, bot_count INTEGER, inbox_failures INTEGER)`)
	days, inst, err := loadFediStatsDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if days != nil || inst != nil {
		t.Fatalf("missing fedi tables should yield nil data, got %v / %v", days, inst)
	}
}

// writeWebDB creates a temporary web.db and returns its path; the writer
// connection is closed so the read-only loaders can open it.
func writeWebDB(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	db.Close()
	return path
}

func TestLoadFediLive(t *testing.T) {
	path := writeWebDB(t,
		`CREATE TABLE followers(id INTEGER, org_id INTEGER, actor_uri TEXT)`,
		`CREATE TABLE delivery_failures(id INTEGER)`,
		`INSERT INTO followers VALUES(1,5,'https://mastodon.social/@a')`,
		`INSERT INTO followers VALUES(2,5,'https://mastodon.social/@b')`,
		`INSERT INTO followers VALUES(3,9,'https://instance.test/@c')`,
		`INSERT INTO delivery_failures VALUES(1)`,
		`INSERT INTO delivery_failures VALUES(2)`,
	)
	snap := loadFediLive(path)
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.TotalFollowers != 3 || snap.PendingDeliveryFailures != 2 {
		t.Fatalf("totals = %d followers / %d failures, want 3/2", snap.TotalFollowers, snap.PendingDeliveryFailures)
	}
	if len(snap.FollowersByOrg) != 2 || snap.FollowersByOrg[0].OrgID != 5 || snap.FollowersByOrg[0].Count != 2 {
		t.Fatalf("followers by org = %+v, want [{5 2} {9 1}] desc", snap.FollowersByOrg)
	}
	if len(snap.TopInstances) != 2 || snap.TopInstances[0].Instance != "mastodon.social" || snap.TopInstances[0].Count != 2 {
		t.Fatalf("top instances = %+v, want [mastodon.social:2 instance.test:1] desc", snap.TopInstances)
	}
}

func TestLoadFediLiveEmptyPath(t *testing.T) {
	if got := loadFediLive(""); got != nil {
		t.Fatalf("loadFediLive(\"\") = %+v, want nil", got)
	}
}

func TestLoadFediLiveMissingTables(t *testing.T) {
	// Query errors must be logged and tolerated — the snapshot still comes
	// back with zero values instead of panicking or dropping the page.
	path := writeWebDB(t, `CREATE TABLE other(id INTEGER)`)
	snap := loadFediLive(path)
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	if snap.TotalFollowers != 0 || len(snap.FollowersByOrg) != 0 || len(snap.TopInstances) != 0 {
		t.Fatalf("expected zero-value snapshot, got %+v", snap)
	}
}
