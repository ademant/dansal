package main

import (
	"errors"
	"sync"
)

// fetchParallel runs fns concurrently and waits for all of them, returning all
// non-nil errors joined (errors.Join). Best-effort fetches whose failure should
// not fail the page return nil from their closure.
func fetchParallel(fns ...func() error) error {
	var errs []error
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(fns))
	for _, fn := range fns {
		go func(f func() error) {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(fn)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// orgMapByID indexes organizations by ID for looking up names during render.
func orgMapByID(orgs []Organization) map[int]Organization {
	m := make(map[int]Organization, len(orgs))
	for _, o := range orgs {
		m[o.ID] = o
	}
	return m
}
