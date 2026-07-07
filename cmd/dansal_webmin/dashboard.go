package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type DansalInfo struct {
	Service         string `json:"service"`
	Version         string `json:"version"`
	BuildTime       string `json:"build_time"`
	TotalEvents     int    `json:"total_events"`
	PublishedEvents int    `json:"published_events"`
	UpcomingEvents  int    `json:"upcoming_events"`
	DBSizeBytes     int64  `json:"db_size_bytes"`
	ImagesSizeBytes int64  `json:"images_size_bytes"`
}

type ServiceStatus struct {
	Name        string
	ActiveState string
	SubState    string
	Description string
}

func (s ServiceStatus) Badge() string {
	switch s.ActiveState {
	case "active":
		return "ok"
	case "failed":
		return "danger"
	case "inactive":
		return "warn"
	default:
		return "warn"
	}
}

type DiskInfo struct {
	Path       string
	TotalBytes uint64
	FreeBytes  uint64
	UsedBytes  uint64
}

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

type DashboardEvent struct {
	ID          int
	Title       string
	DateLabel   string // formatted for display
	Location    string
	Town        string
	IsCancelled bool
	IsPast      bool
}

type DashboardData struct {
	WebminVersion   string
	WebminBuildTime string
	DansalInfo      *DansalInfo
	DansalError     string
	Services        []ServiceStatus
	Disk            *DiskInfo
	Events          []DashboardEvent
	CollectedAt     string
}

func getDashboardEvents(ctx context.Context, dansalURL string) ([]DashboardEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		dansalURL+"/api/v1/events?include_past=true&limit=500", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		StartTime   string `json:"start_time"`
		IsCancelled bool   `json:"is_cancelled"`
		Location    struct {
			Location string `json:"location"`
			Town     string `json:"town"`
		} `json:"location"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	now := time.Now()
	events := make([]DashboardEvent, 0, len(raw))
	for _, e := range raw {
		t, _ := time.Parse(time.RFC3339, e.StartTime)
		events = append(events, DashboardEvent{
			ID:          e.ID,
			Title:       e.Title,
			DateLabel:   t.Format("2006-01-02 15:04"),
			Location:    e.Location.Location,
			Town:        e.Location.Town,
			IsCancelled: e.IsCancelled,
			IsPast:      t.Before(now),
		})
	}
	return events, nil
}

func getDansalInfo(ctx context.Context, dansalURL string) (*DansalInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dansalURL+"/api/v1/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var info DansalInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func monitoredUnits(instance string) []string {
	if instance == "" {
		return []string{
			"dansal", "dansal-web", "dansal-webmin", "dansal-doc",
			"dansal-fetch.timer", "dansal-backup.timer",
			"dansal-vacuum.timer", "dansal-prune-images.timer",
			"dansal-mailcheck.timer",
		}
	}
	sfx := "@" + instance
	return []string{
		"dansal" + sfx,
		"dansal-web" + sfx,
		"dansal-webmin" + sfx,
		"dansal-doc" + sfx,
		"dansal-fetch" + sfx + ".timer",
		"dansal-backup" + sfx + ".timer",
		"dansal-vacuum" + sfx + ".timer",
		"dansal-prune-images" + sfx + ".timer",
		"dansal-mailcheck" + sfx + ".timer",
	}
}

func getServiceStatus(name string) ServiceStatus {
	s := ServiceStatus{Name: name}
	out, err := exec.Command("systemctl", "show", name,
		"--no-pager",
		"--property=ActiveState,SubState,Description",
	).Output()
	if err != nil {
		s.ActiveState = "unknown"
		s.SubState = "systemctl error"
		return s
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			s.ActiveState = v
		case "SubState":
			s.SubState = v
		case "Description":
			s.Description = v
		}
	}
	return s
}

func getDiskInfo(path string) *DiskInfo {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	return &DiskInfo{
		Path:       path,
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  total - free,
	}
}

func collectDashboard(ctx context.Context, cfg *Config) DashboardData {
	d := DashboardData{
		WebminVersion:   Version,
		WebminBuildTime: BuildTime,
		CollectedAt:     time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	}

	info, err := getDansalInfo(ctx, cfg.DansalURL)
	if err != nil {
		d.DansalError = err.Error()
	} else {
		d.DansalInfo = info
	}

	d.Events, _ = getDashboardEvents(ctx, cfg.DansalURL)

	for _, unit := range monitoredUnits(cfg.Instance) {
		d.Services = append(d.Services, getServiceStatus(unit))
	}

	d.Disk = getDiskInfo("/var/lib/dansal")
	return d
}

// fmtBytesTemplate is used in templates via a helper since template funcs aren't set up.
func fmtUint64Bytes(b uint64) string {
	return fmtBytes(int64(b))
}

// renderDashboard writes the data inline - used by templates via .Data accessor.
func dashboardDataMap(d DashboardData) map[string]any {
	var disk map[string]any
	if d.Disk != nil {
		pct := 0
		if d.Disk.TotalBytes > 0 {
			pct = int(d.Disk.UsedBytes * 100 / d.Disk.TotalBytes)
		}
		disk = map[string]any{
			"Path":  d.Disk.Path,
			"Total": fmtBytes(int64(d.Disk.TotalBytes)),
			"Free":  fmtBytes(int64(d.Disk.FreeBytes)),
			"Used":  fmtBytes(int64(d.Disk.UsedBytes)),
			"Pct":   pct,
		}
	}

	var dansalInfo map[string]any
	if d.DansalInfo != nil {
		dansalInfo = map[string]any{
			"Version":         d.DansalInfo.Version,
			"BuildTime":       d.DansalInfo.BuildTime,
			"TotalEvents":     d.DansalInfo.TotalEvents,
			"PublishedEvents": d.DansalInfo.PublishedEvents,
			"UpcomingEvents":  d.DansalInfo.UpcomingEvents,
			"DBSize":          fmtBytes(d.DansalInfo.DBSizeBytes),
			"ImagesSize":      fmtBytes(d.DansalInfo.ImagesSizeBytes),
		}
	}

	pastCount := 0
	for _, e := range d.Events {
		if e.IsPast {
			pastCount++
		}
	}

	return map[string]any{
		"WebminVersion":   d.WebminVersion,
		"WebminBuildTime": d.WebminBuildTime,
		"DansalInfo":      dansalInfo,
		"DansalError":     d.DansalError,
		"Services":        d.Services,
		"Disk":            disk,
		"Events":          d.Events,
		"PastCount":       pastCount,
		"CollectedAt":     d.CollectedAt,
	}
}
