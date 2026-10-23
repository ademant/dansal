package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// iCal feed windowing (#1590). ?days=N limits an .ics feed to events starting
// within the next N days, ?count=N to its next N events. Both may be given and
// the more restrictive one applies. ical_feed_window_days is the operator's
// default window; a request's ?days= replaces it. Only iCal responses are
// windowed. RSS, Atom and JSON Feed are unchanged.

// Built-in bounds, used when ical_max_days or ical_max_count is not set (a
// zero-value Config, e.g. in tests).
const (
	defaultICalMaxDays  = 3650
	defaultICalMaxCount = 1000
)

// icalMaxDays is the upper bound for ?days=.
func (c *Config) icalMaxDays() int {
	if c.ICalMaxDays > 0 {
		return c.ICalMaxDays
	}
	return defaultICalMaxDays
}

// icalMaxCount is the upper bound for ?count=.
func (c *Config) icalMaxCount() int {
	if c.ICalMaxCount > 0 {
		return c.ICalMaxCount
	}
	return defaultICalMaxCount
}

// validateICalWindow rejects bounds that cannot work. A zero ical_feed_window_days
// means no default window.
func (c *Config) validateICalWindow() error {
	if c.ICalMaxDays < 1 || c.ICalMaxCount < 1 {
		return fmt.Errorf("ical_max_days and ical_max_count must be at least 1")
	}
	if c.ICalFeedWindowDays < 0 || c.ICalFeedWindowDays > c.ICalMaxDays {
		return fmt.Errorf("ical_feed_window_days must be 0 (no window) or between 1 and ical_max_days (%d)", c.ICalMaxDays)
	}
	return nil
}

// feedWindow is the window one .ics response applies. Zero means no limit.
type feedWindow struct {
	Days  int
	Count int
}

// parseWindowParam reads one ?days= or ?count= value. It must be a single
// integer from 1 to max; 0, negative, non-numeric and repeated values are
// rejected.
func parseWindowParam(name string, vals []string, max int) (int, error) {
	if len(vals) != 1 {
		return 0, fmt.Errorf("%s must be given once", name)
	}
	n, err := strconv.Atoi(vals[0])
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d", name, max)
	}
	return n, nil
}

// parseFeedWindow reads ?days= and ?count= from a request. Without ?days=, the
// operator's ical_feed_window_days applies. Invalid values are errors, so the
// caller can answer 400.
func parseFeedWindow(q url.Values, cfg *Config) (feedWindow, error) {
	var w feedWindow
	if vals, ok := q["days"]; ok {
		n, err := parseWindowParam("days", vals, cfg.icalMaxDays())
		if err != nil {
			return feedWindow{}, err
		}
		w.Days = n
	} else {
		w.Days = cfg.ICalFeedWindowDays
	}
	if vals, ok := q["count"]; ok {
		n, err := parseWindowParam("count", vals, cfg.icalMaxCount())
		if err != nil {
			return feedWindow{}, err
		}
		w.Count = n
	}
	return w, nil
}

// applyFeedWindow returns the events that fall in the window, soonest first.
// Events without a parseable start are kept and sort last, as before. The input
// slice is not changed.
func applyFeedWindow(events []Event, w feedWindow, now time.Time) []Event {
	out := make([]Event, len(events))
	copy(out, events)
	sort.SliceStable(out, func(i, j int) bool {
		ti, oki := parseEventStart(out[i].StartTime)
		tj, okj := parseEventStart(out[j].StartTime)
		if oki != okj {
			return oki
		}
		return ti.Before(tj)
	})
	if w.Days > 0 {
		end := now.AddDate(0, 0, w.Days)
		kept := out[:0]
		for _, e := range out {
			if t, ok := parseEventStart(e.StartTime); ok && t.After(end) {
				continue
			}
			kept = append(kept, e)
		}
		out = kept
	}
	if w.Count > 0 && len(out) > w.Count {
		out = out[:w.Count]
	}
	return out
}
