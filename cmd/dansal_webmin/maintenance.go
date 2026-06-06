package main

import (
	"encoding/json"
	"fmt"
	"log"
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
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "list-backups"})
		if err == nil && resp.OK {
			json.Unmarshal(resp.Data, &backups)
			for i := range backups {
				backups[i].SizeDisplay = fmtBytes(backups[i].Size)
			}
		}

		d := tmplData(r, cfg, "Maintenance", map[string]any{
			"Flash":   r.URL.Query().Get("flash"),
			"Backups": backups,
		})
		d.User = getSessionUser(r)
		renderTemplate(w, tmpls.maintenance, d)
	}
}

func maintenanceVacuumHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "vacuum"})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("vacuum: %v / %s", err, msg)
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Vacuum error: "+msg), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/maintenance?flash=Database+vacuum+completed", http.StatusSeeOther)
	}
}

func maintenancePruneImagesHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "prune-images"})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("prune-images: %v / %s", err, msg)
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Prune error: "+msg), http.StatusSeeOther)
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
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "fetch-all"})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("fetch-all: %v / %s", err, msg)
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Fetch error: "+msg), http.StatusSeeOther)
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
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "list-backups"})
		if err != nil || !resp.OK {
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Could not list backups"), http.StatusSeeOther)
			return
		}
		var files []backupFileInfo
		json.Unmarshal(resp.Data, &files)
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

		// We need to pass the full path. Ask the API to resolve it via list-backups
		// which already uses the configured backup_dir. Build path from first file's
		// ModTime is not reliable — instead send the filename; the API will join it
		// with its configured backup_dir in restoreFromTar if the path has no dir component.
		// Simpler: send the bare filename and let the API prepend backup_dir.
		resp2, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "restore", Path: filename})
		if err != nil || !resp2.OK {
			msg := "socket error"
			if err == nil {
				msg = resp2.Error
			}
			log.Printf("restore: %v / %s", err, msg)
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Restore error: "+msg), http.StatusSeeOther)
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
		resp, err := sendSocket(cfg.AdminSocket, socketRequest{Cmd: "backup", Path: outputPath})
		if err != nil || !resp.OK {
			msg := "socket error"
			if err == nil {
				msg = resp.Error
			}
			log.Printf("backup: %v / %s", err, msg)
			http.Redirect(w, r, "/maintenance?flash="+url.QueryEscape("Backup error: "+msg), http.StatusSeeOther)
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
