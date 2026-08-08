package main

import (
	"sync"
	"time"
)

type submissionThrottle struct {
	mu          sync.Mutex
	entries     map[string]*submissionEntry
	limit       int
	window      time.Duration
	forgetAfter time.Duration
}

type submissionEntry struct {
	count       int
	windowStart time.Time
}

func newSubmissionThrottle(limit int, window time.Duration) *submissionThrottle {
	return newSubmissionThrottleForget(limit, window, window)
}

func newSubmissionThrottleForget(limit int, window, forgetAfter time.Duration) *submissionThrottle {
	st := &submissionThrottle{
		entries:     make(map[string]*submissionEntry),
		limit:       limit,
		window:      window,
		forgetAfter: forgetAfter,
	}
	if window > 0 {
		go st.sweep()
	}
	return st
}

func (st *submissionThrottle) isBlocked(ip string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.entries[ip]
	return e != nil && time.Since(e.windowStart) <= st.window && e.count >= st.limit
}

func (st *submissionThrottle) record(ip string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.entries[ip]
	if e == nil {
		e = &submissionEntry{windowStart: time.Now()}
		st.entries[ip] = e
	} else if time.Since(e.windowStart) > st.window {
		e.count = 0
		e.windowStart = time.Now()
	}
	if e.count < st.limit {
		e.count++
	}
}

func (st *submissionThrottle) sweep() {
	ticker := time.NewTicker(st.window)
	defer ticker.Stop()
	for range ticker.C {
		st.mu.Lock()
		now := time.Now()
		for key, e := range st.entries {
			if now.Sub(e.windowStart) > st.forgetAfter {
				delete(st.entries, key)
			}
		}
		st.mu.Unlock()
	}
}
