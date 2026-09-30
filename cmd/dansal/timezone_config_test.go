package main

// server.timezone (#1394): the instance-wide zone every timezone-less event
// input is parsed in and every stored epoch is rendered in. events.start_time
// is a timezone-neutral Unix epoch, so this is purely an interpretation/
// display setting, not a per-row column — see instanceTimezone/instanceLoc
// (ical_time.go), which every parsing/rendering call site already goes
// through, and validateInstanceTimezone (config.go), the single place both
// startup and a SIGHUP reload validate it.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v2"
)

// TestPackagingConfigParsesTimezone matches config_checkin_test.go's own
// convention: the documented default in packaging/config.yaml must actually
// parse into the config struct, or the shipped docs describe a setting that
// silently does nothing.
func TestPackagingConfigParsesTimezone(t *testing.T) {
	src := filepath.Join("..", "..", "packaging", "config.yaml")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("packaging/config.yaml not available: %v", err)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(abs)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Server.Timezone != "Europe/Berlin" {
		t.Errorf("packaging/config.yaml timezone = %q, want Europe/Berlin", cfg.Server.Timezone)
	}
	if _, err := validateInstanceTimezone(cfg.Server.Timezone); err != nil {
		t.Errorf("packaging/config.yaml's documented timezone does not validate: %v", err)
	}
}

func TestServerTimezoneDefaultsToBerlin(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Server.Timezone != "Europe/Berlin" {
		t.Errorf("default server.timezone = %q, want Europe/Berlin", cfg.Server.Timezone)
	}
}

func TestServerTimezoneExplicitValuePreserved(t *testing.T) {
	cfg := &Config{}
	cfg.Server.Timezone = "America/New_York"
	applyDefaults(cfg)
	if cfg.Server.Timezone != "America/New_York" {
		t.Errorf("server.timezone = %q, want America/New_York preserved", cfg.Server.Timezone)
	}
}

func TestValidateInstanceTimezone(t *testing.T) {
	if _, err := validateInstanceTimezone("America/New_York"); err != nil {
		t.Errorf("valid IANA zone rejected: %v", err)
	}
	_, err := validateInstanceTimezone("Not/AZone")
	if err == nil {
		t.Fatal("invalid IANA zone accepted, want an error")
	}
	// The error must name the setting, not just repeat Go's bare
	// "unknown time zone" message — an admin reading a startup failure log
	// needs to know which config key to fix.
	if got := err.Error(); !contains(got, "server.timezone") || !contains(got, "Not/AZone") {
		t.Errorf("error = %q, want it to name server.timezone and the bad value", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// withInstanceTimezone swaps the package-level instanceTimezone for the
// duration of the test, restoring it afterward — the same injection point
// production startup/reload use.
func withInstanceTimezone(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time.LoadLocation(%q): %v", name, err)
	}
	old := instanceTimezone
	instanceTimezone = loc
	t.Cleanup(func() { instanceTimezone = old })
}

// TestParseAndRenderUseConfiguredNonBerlinZone covers the issue's core
// acceptance criterion directly: naive (timezone-less) input is parsed as the
// *configured* zone, not hardcoded Berlin, and epochToLocal renders that same
// epoch back with the configured zone's offset — exercised across a
// DST-observing zone genuinely different from Berlin's own DST calendar.
func TestParseAndRenderUseConfiguredNonBerlinZone(t *testing.T) {
	withInstanceTimezone(t, "America/New_York")

	cases := []struct {
		name       string
		naive      string // no offset: must be parsed as America/New_York
		wantOffset string // expected RFC3339 offset after render
	}{
		{"EST (winter)", "2026-01-15T10:00:00", "-05:00"},
		{"EDT (summer)", "2026-07-15T10:00:00", "-04:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			epoch, err := parseTimeToUnix(c.naive)
			if err != nil {
				t.Fatalf("parseTimeToUnix(%q): %v", c.naive, err)
			}
			rendered := epochToLocal(epoch)
			got, err := time.Parse(time.RFC3339, rendered)
			if err != nil {
				t.Fatalf("epochToLocal produced unparseable output %q: %v", rendered, err)
			}
			_, offsetSecs := got.Zone()
			wantDur, _ := time.ParseDuration(c.wantOffset[:3] + "h" + c.wantOffset[4:] + "m")
			if int(wantDur.Seconds()) != offsetSecs {
				t.Errorf("rendered %q, want offset %s (got %+d seconds)", rendered, c.wantOffset, offsetSecs)
			}
			// The wall-clock hour must round-trip: 10:00 local in, 10:00
			// local out, regardless of which side of DST it fell on.
			if got.Hour() != 10 {
				t.Errorf("rendered %q, want local hour 10, got %d", rendered, got.Hour())
			}
		})
	}
}

// TestExplicitOffsetAndUTCPreserveInstantRegardlessOfInstanceZone covers the
// other half of the same criterion: a caller that already supplied an
// explicit offset (or "Z") must keep its exact absolute instant no matter
// what server.timezone is configured to — only naive input is reinterpreted.
func TestExplicitOffsetAndUTCPreserveInstantRegardlessOfInstanceZone(t *testing.T) {
	withInstanceTimezone(t, "Pacific/Auckland") // as far from Berlin as practical

	cases := []string{
		"2026-06-01T12:00:00Z",
		"2026-06-01T14:00:00+02:00", // same instant: 14:00 minus 2h
		"2026-06-01T07:00:00-05:00", // same instant: 07:00 plus 5h
	}
	want, _ := time.Parse(time.RFC3339, "2026-06-01T12:00:00Z")
	for _, s := range cases {
		epoch, err := parseTimeToUnix(s)
		if err != nil {
			t.Fatalf("parseTimeToUnix(%q): %v", s, err)
		}
		if got := time.Unix(epoch, 0).UTC(); !got.Equal(want) {
			t.Errorf("parseTimeToUnix(%q) = %v, want the same instant as %v", s, got, want)
		}
	}
}

// TestFloatingICalAnchoredInConfiguredZone covers the iCal-specific
// acceptance criterion: a floating (zone-less) DTSTART is anchored in
// server.timezone, independent of the host process's own OS timezone —
// icalOccurrenceTime (ical_time.go) is what parseICalToRequests/parseICalBody
// both call for this.
func TestFloatingICalAnchoredInConfiguredZone(t *testing.T) {
	withInstanceTimezone(t, "America/New_York")

	// A floating instant: year/month/day/hour/min/sec taken as-is, then
	// anchored in instanceLoc() — mirrors how a floating DTSTART is built
	// from golang-ical's own (host-TZ) parse before being re-anchored.
	naive := time.Date(2026, 7, 15, 20, 0, 0, 0, time.Local)
	anchored := icalOccurrenceTime(naive, true, instanceLoc())

	if zone, _ := anchored.Zone(); zone != "EDT" && zone != "EST" {
		t.Errorf("anchored.Zone() = %q, want an America/New_York abbreviation", zone)
	}
	if anchored.Hour() != 20 || anchored.Minute() != 0 {
		t.Errorf("anchored wall clock = %02d:%02d, want 20:00 (the floating value, unchanged)", anchored.Hour(), anchored.Minute())
	}
	if anchored.Location() != instanceLoc() {
		t.Error("anchored time is not in the configured instance zone")
	}
}

// TestAdminTimezoneGetSet covers the dansal-webmin bridge (#1394): the
// admin-socket "timezone-get"/"timezone-set" commands, the only way webmin
// can read or change server.timezone since it has no file access of its own
// and no restart mechanism — the whole point of doing the write-and-apply
// inside the dansal process itself.
func TestAdminTimezoneGetSet(t *testing.T) {
	oldConfig, oldPath, oldTZ := config, configFilePath, instanceTimezone
	t.Cleanup(func() { config, configFilePath, instanceTimezone = oldConfig, oldPath, oldTZ })

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	config = &Config{}
	config.Server.Timezone = "Europe/Berlin"
	data, _ := yaml.Marshal(config)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	configFilePath = path
	instanceTimezone, _ = time.LoadLocation("Europe/Berlin")

	t.Run("get reports the current value", func(t *testing.T) {
		resp := adminTimezoneGet()
		if !resp.OK {
			t.Fatalf("adminTimezoneGet: %+v", resp)
		}
		got := resp.Data.(map[string]string)["timezone"]
		if got != "Europe/Berlin" {
			t.Errorf("timezone = %q, want Europe/Berlin", got)
		}
	})

	t.Run("set with a valid zone persists, applies live, and get reflects it", func(t *testing.T) {
		resp := adminTimezoneSet(adminRequest{Cmd: "timezone-set", Timezone: "America/New_York"})
		if !resp.OK {
			t.Fatalf("adminTimezoneSet: %+v", resp)
		}
		if config.Server.Timezone != "America/New_York" {
			t.Errorf("config.Server.Timezone = %q, want America/New_York", config.Server.Timezone)
		}
		if instanceLoc().String() != "America/New_York" {
			t.Errorf("instanceLoc() = %v, want the change to apply live with no restart", instanceLoc())
		}
		reread, err := loadConfig(path)
		if err != nil {
			t.Fatalf("re-reading the saved config: %v", err)
		}
		if reread.Server.Timezone != "America/New_York" {
			t.Errorf("saved config.yaml timezone = %q, want America/New_York", reread.Server.Timezone)
		}
		if got := adminTimezoneGet().Data.(map[string]string)["timezone"]; got != "America/New_York" {
			t.Errorf("adminTimezoneGet after set = %q, want America/New_York", got)
		}
	})

	t.Run("set with an invalid zone changes nothing", func(t *testing.T) {
		before := config.Server.Timezone
		resp := adminTimezoneSet(adminRequest{Cmd: "timezone-set", Timezone: "Not/AZone"})
		if resp.OK {
			t.Fatal("adminTimezoneSet accepted an invalid IANA zone")
		}
		if config.Server.Timezone != before {
			t.Errorf("config.Server.Timezone changed to %q despite the rejected set", config.Server.Timezone)
		}
	})

	t.Run("set with an empty zone is rejected", func(t *testing.T) {
		resp := adminTimezoneSet(adminRequest{Cmd: "timezone-set"})
		if resp.OK {
			t.Fatal("adminTimezoneSet accepted an empty timezone")
		}
	})
}
