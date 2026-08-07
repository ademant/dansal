package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type backupFileInfo struct {
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	SizeDisplay string    `json:"-"`
}

func maintenancePageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Load backup list for the restore card.
		var backups []backupFileInfo
		if err := getSocketData(cfg.AdminSocket, "list-backups", &backups); err == nil {
			for i := range backups {
				backups[i].SizeDisplay = fmtBytes(backups[i].Size)
			}
		}

		d := tmplData(r, cfg, "Maintenance", map[string]any{
			"Flash":   r.URL.Query().Get("flash"),
			"Backups": backups,
		})
		renderTemplate(w, tmpls.maintenance, d)
	}
}

func maintenanceVacuumHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, ok := socketFlashRedirect(w, r, cfg, "/maintenance", "Vacuum error", socketRequest{Cmd: "vacuum"})
		if !ok {
			return
		}
		http.Redirect(w, r, "/maintenance?flash=Database+vacuum+completed", http.StatusSeeOther)
	}
}

func maintenancePruneImagesHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, ok := socketFlashRedirect(w, r, cfg, "/maintenance", "Prune error", socketRequest{Cmd: "prune-images"})
		if !ok {
			return
		}
		var result struct {
			Removed    int   `json:"removed"`
			FreedBytes int64 `json:"freed_bytes"`
		}
		json.Unmarshal(resp.Data, &result)
		msg := fmt.Sprintf("Removed %d orphaned image(s), freed %s", result.Removed, fmtBytes(result.FreedBytes))
		http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func maintenanceFetchAllHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, ok := socketFlashRedirect(w, r, cfg, "/maintenance", "Fetch error", socketRequest{Cmd: "fetch-all"})
		if !ok {
			return
		}
		var results []struct {
			ID     int    `json:"id"`
			URL    string `json:"url"`
			Events int    `json:"events"`
			Error  string `json:"error,omitempty"`
		}
		json.Unmarshal(resp.Data, &results)
		total, errCount := 0, 0
		for _, r := range results {
			if r.Error != "" {
				errCount++
			} else {
				total += r.Events
			}
		}
		msg := fmt.Sprintf("Fetched %d source(s): %d event(s), %d error(s)", len(results), total, errCount)
		http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func maintenanceRestoreHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		filename := r.FormValue("filename")
		// Reject anything that looks like a path traversal.
		if filename == "" || strings.ContainsAny(filename, "/\\") {
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Invalid filename"), http.StatusSeeOther)
			return
		}

		// Ask the API for its backup dir so we can build the full path server-side.
		var files []backupFileInfo
		if err := getSocketData(cfg.AdminSocket, "list-backups", &files); err != nil {
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Could not list backups"), http.StatusSeeOther)
			return
		}
		var found bool
		for _, f := range files {
			if f.Name == filename {
				found = true
				break
			}
		}
		if !found {
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Backup file not found: "+filename), http.StatusSeeOther)
			return
		}

		// Send the bare filename; the API joins it with its configured backup_dir.
		resp2, ok := socketFlashRedirect(w, r, cfg, "/maintenance", "Restore error", socketRequest{Cmd: "restore", Path: filename})
		if !ok {
			return
		}
		var result struct {
			Config bool `json:"config"`
			DB     bool `json:"db"`
			Images int  `json:"images"`
		}
		json.Unmarshal(resp2.Data, &result)
		msg := fmt.Sprintf("Restore complete — DB: %v, config: %v, images: %d", result.DB, result.Config, result.Images)
		http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func maintenanceBackupHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		outputPath := r.FormValue("output")
		resp, ok := socketFlashRedirect(w, r, cfg, "/maintenance", "Backup error", socketRequest{Cmd: "backup", Path: outputPath})
		if !ok {
			return
		}
		var result struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		}
		json.Unmarshal(resp.Data, &result)
		msg := fmt.Sprintf("Backup written to %s (%s)", result.Path, fmtBytes(result.Size))
		http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}
