package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetricsServer creates an HTTP server that serves Prometheus metrics.
func NewMetricsServer(addr string, health *HealthChecker) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	if health != nil {
		mux.HandleFunc("/health", health.HealthHandler)
		mux.HandleFunc("/ready", health.ReadinessHandler)
	}

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}
