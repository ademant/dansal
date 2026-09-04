package main

import (
	"context"
	"fmt"
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
	Path        string
	TotalBytes  int64
	FreeBytes   int64
	UsedBytes   int64
	UsedPercent int
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
	PastCount       int
	CollectedAt     string
}

func getDashboardEvents(ctx context.Context, dansalURL string, orgID int) ([]DashboardEvent, error) {
	u := dansalURL + "/api/v1/events?include_past=true&limit=500"
	if orgID > 0 {
		u += "&org_id=" + fmt.Sprintf("%d", orgID)
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
	if err := getJSON(ctx, u, &raw); err != nil {
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
	var info DansalInfo
	if err := getJSON(ctx, dansalURL+"/api/v1/info", &info); err != nil {
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
	for line := range strings.SplitSeq(string(out), "\n") {
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
	used := total - free
	pct := 0
	if total > 0 {
		pct = int(used * 100 / total)
	}
	return &DiskInfo{
		Path:        path,
		TotalBytes:  int64(total),
		FreeBytes:   int64(free),
		UsedBytes:   int64(used),
		UsedPercent: pct,
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

	d.Events, _ = getDashboardEvents(ctx, cfg.DansalURL, cfg.OrgID)
	for _, e := range d.Events {
		if e.IsPast {
			d.PastCount++
		}
	}

	for _, unit := range monitoredUnits(cfg.Instance) {
		d.Services = append(d.Services, getServiceStatus(unit))
	}

	d.Disk = getDiskInfo("/var/lib/dansal")
	return d
}
