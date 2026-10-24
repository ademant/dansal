package main

import (
	"errors"
	"github.com/ademant/dansal/internal/logging"
	"time"
)

// Per-source date window (#1578): a fetch source imports only events whose
// start falls between maxPastDays ago and maxFutureDays ahead. This drops old
// archive entries and far-future noise from sloppy feeds before dedup.

const (
	defaultFetchMaxPastDays   = 1
	defaultFetchMaxFutureDays = 365
	// fetchWindowMaxDays bounds either side; a window wider than ten years
	// is not a window.
	fetchWindowMaxDays = 3650
)

var errFetchWindowRange = errors.New("max_past_days and max_future_days must be between 0 and 3650")

// validateFetchWindow checks the optional window values from a request. nil
// means "leave the setting as it is".
func validateFetchWindow(past, future *int) error {
	for _, v := range []*int{past, future} {
		if v != nil && (*v < 0 || *v > fetchWindowMaxDays) {
			return errFetchWindowRange
		}
	}
	return nil
}

// setFetchSourceWindow stores the window values that are set. Unset values
// keep their current setting.
func setFetchSourceWindow(sourceID int, past, future *int) error {
	if past != nil {
		if _, err := db.Exec("UPDATE fetch_sources SET max_past_days = ? WHERE id = ?", *past, sourceID); err != nil {
			return err
		}
	}
	if future != nil {
		if _, err := db.Exec("UPDATE fetch_sources SET max_future_days = ? WHERE id = ?", *future, sourceID); err != nil {
			return err
		}
	}
	return nil
}

// filterEntriesToWindow keeps the import entries whose start time is inside the
// source's window. Entries whose start can't be parsed are kept, so a malformed
// date never drops an event silently.
func filterEntriesToWindow[T any](src FetchSource, entries []T, reqOf func(T) EventCreateRequest, now time.Time) []T {
	// Both zero means no window was ever set on this struct (for example one
	// built in code rather than read from the table), so use the defaults.
	// A source with a real window keeps it, zeros included.
	past, future := src.MaxPastDays, src.MaxFutureDays
	if past == 0 && future == 0 {
		past, future = defaultFetchMaxPastDays, defaultFetchMaxFutureDays
	}
	earliest := now.Add(-time.Duration(past) * 24 * time.Hour)
	latest := now.Add(time.Duration(future) * 24 * time.Hour)

	kept := make([]T, 0, len(entries))
	dropped := 0
	for _, e := range entries {
		start, err := time.Parse(time.RFC3339, reqOf(e).StartTime)
		if err != nil || (!start.Before(earliest) && !start.After(latest)) {
			kept = append(kept, e)
			continue
		}
		dropped++
	}
	if dropped > 0 {
		logging.Infof("fetch source %d: %d entries outside the import window dropped", src.ID, dropped)
	}
	return kept
}
