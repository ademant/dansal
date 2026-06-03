package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
)

func maintenancePageHandler(cfg *Config, tmpls *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := tmplData(r, cfg, "Maintenance", map[string]string{
			"Flash": r.URL.Query().Get("flash"),
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
