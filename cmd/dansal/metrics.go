package main

import (
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	metricRegistry = prometheus.NewRegistry()

	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dansal_http_requests_total",
			Help: "Total HTTP requests by method, route, and status code.",
		},
		[]string{"method", "route", "status"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dansal_http_request_duration_seconds",
			Help:    "HTTP request latency distribution.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)

	rateLimitRejectionsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "dansal_rate_limit_rejections_total",
			Help: "Total requests rejected by the rate limiter.",
		},
	)
)

func initMetrics() {
	metricRegistry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequestsTotal,
		httpRequestDuration,
		rateLimitRejectionsTotal,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dansal_db_open_connections",
			Help: "Number of open database connections.",
		}, func() float64 { return float64(db.Stats().OpenConnections) }),
		dbCountGauge("dansal_db_events_total", "Total number of events.", "events"),
		dbCountGauge("dansal_db_users_total", "Total number of users.", "users"),
		dbCountGauge("dansal_db_locations_total", "Total number of locations.", "locations"),
	)
}

func dbCountGauge(name, help, table string) prometheus.GaugeFunc {
	return prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{Name: name, Help: help},
		func() float64 {
			var n int64
			db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n) //nolint:gosec // table is a hardcoded literal
			return float64(n)
		},
	)
}

// statusCapture wraps http.ResponseWriter to capture the written status code.
type statusCapture struct {
	http.ResponseWriter
	status int
}

func (sc *statusCapture) WriteHeader(code int) {
	sc.status = code
	sc.ResponseWriter.WriteHeader(code)
}

func (sc *statusCapture) Write(b []byte) (int, error) {
	if sc.status == 0 {
		sc.status = http.StatusOK
	}
	return sc.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (e.g. SetWriteDeadline) see through
// this wrapper to the underlying ResponseWriter, per its documented contract.
func (sc *statusCapture) Unwrap() http.ResponseWriter { return sc.ResponseWriter }

// MetricsMiddleware records per-request HTTP metrics. It accepts the ServeMux
// so it can resolve the matched route pattern for accurate label cardinality.
// Register it as the outermost wrapper so it captures status codes from all
// early-exit paths (rate limiting, connection limits, etc.).
func MetricsMiddleware(smux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sc := &statusCapture{ResponseWriter: w}
			next.ServeHTTP(sc, r)

			status := sc.status
			if status == 0 {
				status = http.StatusOK
			}

			route := "unknown"
			if _, pattern := smux.Handler(r); pattern != "" {
				// Strip leading method prefix added by Go 1.22+ patterns ("GET /path").
				if _, after, ok := strings.Cut(pattern, " "); ok {
					route = after
				} else {
					route = pattern
				}
			}

			httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
			httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

func startMetricsServer() {
	if config.Server.MetricsPort == 0 {
		return
	}

	handler := promhttp.HandlerFor(metricRegistry, promhttp.HandlerOpts{})
	if len(config.Server.MetricsAllowedIPs) > 0 {
		handler = metricsIPGuard(handler)
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", handler)

	addr := "127.0.0.1:" + strconv.Itoa(config.Server.MetricsPort)
	srv := &http.Server{
		Addr:         addr,
		Handler:      metricsMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("Metrics server starting on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Metrics server error: %v", err)
		}
	}()
}

func metricsIPGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if slices.Contains(config.Server.MetricsAllowedIPs, ip) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, "Forbidden", http.StatusForbidden)
	})
}
