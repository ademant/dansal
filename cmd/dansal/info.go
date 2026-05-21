package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type ServiceInfo struct {
	Service                 string `json:"service"`
	Version                 string `json:"version"`
	BuildTime               string `json:"build_time"`
	TotalEvents             int    `json:"total_events"`
	PublishedEvents         int    `json:"published_events"`
	UpcomingEvents          int    `json:"upcoming_events"`
	DBSizeBytes             int64  `json:"db_size_bytes"`
	ImagesSizeBytes         int64  `json:"images_size_bytes"`
	SelfRegistrationEnabled bool   `json:"self_registration_enabled"`
}

// GET /api/v1/info
func getInfo(w http.ResponseWriter, r *http.Request) {
	var total, published, upcoming int

	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&total); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE is_published = 1`).Scan(&published); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE is_published = 1 AND start_time > ?`, now()).Scan(&upcoming); err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var dbSize int64
	if fi, err := os.Stat(config.Server.DBPath); err == nil {
		dbSize = fi.Size()
	}
	var imagesSize int64
	filepath.WalkDir(config.Server.ImagesDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				imagesSize += fi.Size()
			}
		}
		return nil
	})

	info := ServiceInfo{
		Service:                 "dansal",
		Version:                 Version,
		BuildTime:               BuildTime,
		TotalEvents:             total,
		PublishedEvents:         published,
		UpcomingEvents:          upcoming,
		DBSizeBytes:             dbSize,
		ImagesSizeBytes:         imagesSize,
		SelfRegistrationEnabled: selfRegEnabled(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func now() int64 {
	return time.Now().Unix()
}
