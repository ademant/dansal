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
	Service                  string `json:"service"`
	Version                  string `json:"version"`
	BuildTime                string `json:"build_time"`
	TotalEvents              int    `json:"total_events"`
	PublishedEvents          int    `json:"published_events"`
	UpcomingEvents           int    `json:"upcoming_events"`
	TotalUsers               int    `json:"total_users"`
	BoardEntries             int    `json:"board_entries"`
	TotalOrganizations       int    `json:"total_organizations"`
	TotalLocations           int    `json:"total_locations"`
	DBSizeBytes              int64  `json:"db_size_bytes"`
	ImagesSizeBytes          int64  `json:"images_size_bytes"`
	SelfRegistrationEnabled  bool   `json:"self_registration_enabled"`
	TelegramChannelAvailable bool   `json:"telegram_channel_available"`
}

// GET /api/v1/info
func getInfo(w http.ResponseWriter, r *http.Request) {
	var total, published, upcoming int

	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&total); err != nil {
		writeInternalError(w, err)
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE is_published = 1`).Scan(&published); err != nil {
		writeInternalError(w, err)
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE is_published = 1 AND start_time > ?`, now()).Scan(&upcoming); err != nil {
		writeInternalError(w, err)
		return
	}

	var totalUsers, boardEntries, totalOrgs, totalLocations int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE disabled=0 AND email_verified=1`).Scan(&totalUsers)
	db.QueryRow(`SELECT COUNT(*) FROM contact_posts WHERE email_verified=1`).Scan(&boardEntries)
	db.QueryRow(`SELECT COUNT(*) FROM organizations`).Scan(&totalOrgs)
	db.QueryRow(`SELECT COUNT(*) FROM locations`).Scan(&totalLocations)

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
		Service:                  "dansal",
		Version:                  Version,
		BuildTime:                BuildTime,
		TotalEvents:              total,
		PublishedEvents:          published,
		UpcomingEvents:           upcoming,
		TotalUsers:               totalUsers,
		BoardEntries:             boardEntries,
		TotalOrganizations:       totalOrgs,
		TotalLocations:           totalLocations,
		DBSizeBytes:              dbSize,
		ImagesSizeBytes:          imagesSize,
		SelfRegistrationEnabled:  selfRegEnabled(),
		TelegramChannelAvailable: config.Server.TelegramBotToken != "",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func now() int64 {
	return time.Now().Unix()
}
