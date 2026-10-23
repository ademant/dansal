package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// The org actor document (#1614) is public, but building it costs an API
// round trip for the organisation. A fan-out burst after a post asks for the
// same actor from hundreds of instances, so the marshalled document is cached
// per slug for apActorCacheTTL, the max-age the handler already advertises.
// Organisation saves clear the cache.

// apActorCacheTTL matches the Cache-Control max-age on actor responses.
const apActorCacheTTL = 5 * time.Minute

type apActorEntry struct {
	body    []byte
	expires time.Time
}

// apActorCache: key is the org slug (string), value apActorEntry.
var apActorCache sync.Map

// getAPActorBody returns the cached actor document for slug while it is fresh.
func getAPActorBody(slug string) ([]byte, bool) {
	v, ok := apActorCache.Load(slug)
	if !ok {
		return nil, false
	}
	e := v.(apActorEntry)
	if time.Now().After(e.expires) {
		apActorCache.Delete(slug)
		return nil, false
	}
	return e.body, true
}

// putAPActorBody stores the actor document for slug for apCacheTTL.
func putAPActorBody(slug string, body []byte) {
	apActorCache.Store(slug, apActorEntry{body: body, expires: time.Now().Add(apActorCacheTTL)})
}

// invalidateAPActorCache drops every cached actor document. An organisation
// save can change any of them (name, avatar, the key that signs deliveries).
func invalidateAPActorCache() {
	apActorCache.Range(func(k, _ any) bool {
		apActorCache.Delete(k)
		return true
	})
}

// writeAPActorBody writes a marshalled actor document as ActivityPub JSON.
func writeAPActorBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/activity+json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// writeAPActor marshals v, caches it under slug and writes it.
func writeAPActor(w http.ResponseWriter, slug string, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "actor encode error", http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')
	putAPActorBody(slug, body)
	writeAPActorBody(w, body)
}
