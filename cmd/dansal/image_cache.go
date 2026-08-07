package main

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

// scanIDFiles scans dir for files named "{id}{suffix}" and calls fn for each
// numeric id found. Shared by every id-keyed image/avatar cache's startup
// scan (#1010) — silently returns if dir doesn't exist yet.
func scanIDFiles(dir, suffix string, fn func(id int)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		if id, err := strconv.Atoi(strings.TrimSuffix(name, suffix)); err == nil {
			fn(id)
		}
	}
}

// scanImageIDs scans dir for files with either configured image extension
// (".avif" or ".jpeg") and calls fn with each numeric id and the extension
// it was found under.
func scanImageIDs(dir string, fn func(id int, ext string)) {
	scanIDFiles(dir, ".avif", func(id int) { fn(id, ".avif") })
	scanIDFiles(dir, ".jpeg", func(id int) { fn(id, ".jpeg") })
}

// imageIDCache is an in-memory set of entity IDs that have an image file on
// disk. It avoids an os.Stat syscall per row when building list responses.
// Shared by event/musician/location image caches (#1010); org images and
// avatars layer their own value type (mimeType, dir/urlBase) on top of the
// same scan/lock pattern rather than reusing this type directly, since they
// need per-id data beyond presence.
type imageIDCache struct {
	mu  sync.RWMutex
	ids map[int]struct{}
}

func newImageIDCache() *imageIDCache {
	return &imageIDCache{ids: make(map[int]struct{})}
}

// init (re)populates the cache by scanning dir. Called once at startup;
// the cache is kept up-to-date afterwards via add/remove.
func (c *imageIDCache) init(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scanImageIDs(dir, func(id int, _ string) { c.ids[id] = struct{}{} })
}

func (c *imageIDCache) has(id int) bool {
	c.mu.RLock()
	_, ok := c.ids[id]
	c.mu.RUnlock()
	return ok
}

func (c *imageIDCache) add(id int) {
	c.mu.Lock()
	c.ids[id] = struct{}{}
	c.mu.Unlock()
}

func (c *imageIDCache) remove(id int) {
	c.mu.Lock()
	delete(c.ids, id)
	c.mu.Unlock()
}
