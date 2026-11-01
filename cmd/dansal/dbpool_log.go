package main

import (
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/ademant/dansal/internal/logging"
)

// dbPoolLogInterval is how often startDBPoolLogger writes the pool state.
const dbPoolLogInterval = 30 * time.Second

// httpInFlight counts requests inside MetricsMiddleware. httpReqSeq numbers
// them, so a "start" debug line can be matched to its "done" line (#1706).
var (
	httpInFlight atomic.Int64
	httpReqSeq   atomic.Uint64
)

// startDBPoolLogger logs the connection pool state at debug level every
// dbPoolLogInterval (#1706). Wait counts are deltas since the previous sample,
// so a stalled pool shows as a growing wait_ms with in_use at the limit.
func startDBPoolLogger() {
	go func() {
		ticker := time.NewTicker(dbPoolLogInterval)
		defer ticker.Stop()
		var prev sql.DBStats
		for range ticker.C {
			s := db.Stats()
			logging.Debugf("db pool: open=%d in_use=%d idle=%d wait_count=%d wait_ms=%d http_in_flight=%d",
				s.OpenConnections, s.InUse, s.Idle,
				s.WaitCount-prev.WaitCount,
				(s.WaitDuration - prev.WaitDuration).Milliseconds(),
				httpInFlight.Load())
			prev = s
			logTrackedDB()
		}
	}()
}
