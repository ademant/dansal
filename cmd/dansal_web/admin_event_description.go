package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// EventDescriptionPageData holds what the dedicated description editor
// template needs.
type EventDescriptionPageData struct {
	Event Event
}

// GET /admin/events/{id}/description — serve the dedicated description
// editor (#1159): a formatting toolbar + live preview + always-available
// markdown cheat-sheet, replacing the "?" the user remembered from before
// the live preview started hiding the inline cheat-sheet sidebar.
func adminEventDescriptionPageHandler(cfg *Config, tmpls *Templates, client *DansalClient, i18n *I18n) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)
		event, err := client.GetEventAuthed(r.Context(), id, tok)
		if err != nil {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		title := "Description — " + event.Title
		renderTemplate(w, tmpls.adminEventDescription, tmplData(r, cfg, i18n, title, EventDescriptionPageData{
			Event: event,
		}))
	}
}

// POST /admin/events/{id}/description — save just the description field
// (proxy to the API's merge-patch endpoint).
func adminEventDescriptionSaveHandler(client *DansalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := requireLogin(w, r)
		if !ok {
			return
		}
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid event id", http.StatusBadRequest)
			return
		}
		tok := getSessionToken(r)

		var req struct {
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := client.PatchEventDescription(r.Context(), id, req.Description, tok); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}
}
