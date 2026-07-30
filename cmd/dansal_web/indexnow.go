package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// indexNowKeyFileHandler serves /{key}.txt for Bing/Yandex ownership verification.
// The key is read dynamically from site_settings so webmin changes take effect
// within the siteSettingsCache TTL without a restart.
func indexNowKeyFileHandler(w http.ResponseWriter, r *http.Request) {
	key := siteCfg.IndexNowKey()
	name := r.PathValue("name")
	if key == "" || name != key+".txt" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, key)
}

// notifyIndexNow POSTs the given event IDs to the IndexNow API. Must be called
// in a goroutine — never blocks the HTTP handler.
func notifyIndexNow(baseURL, key string, ids []int) {
	if len(ids) == 0 {
		return
	}
	urls := make([]string, len(ids))
	for i, id := range ids {
		urls[i] = fmt.Sprintf("%s/events/%d", baseURL, id)
	}
	notifyIndexNowURLs(baseURL, key, urls)
}

// notifyIndexNowPaths is like notifyIndexNow but for non-event pages — pass
// site-relative paths (e.g. "/location/5", "/org/some-slug"). Must be called
// in a goroutine — never blocks the HTTP handler.
func notifyIndexNowPaths(baseURL, key string, paths []string) {
	if len(paths) == 0 {
		return
	}
	urls := make([]string, len(paths))
	for i, p := range paths {
		urls[i] = baseURL + p
	}
	notifyIndexNowURLs(baseURL, key, urls)
}

func notifyIndexNowURLs(baseURL, key string, urls []string) {
	if key == "" || len(urls) == 0 {
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	body, _ := json.Marshal(map[string]any{
		"host":    host,
		"key":     key,
		"urlList": urls,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.indexnow.org/indexnow", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("indexnow: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("indexnow: status %d for %d URL(s)", resp.StatusCode, len(urls))
	}
}
