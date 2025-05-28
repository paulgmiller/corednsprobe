// Package metrics provides Prometheus metrics for CoreDNS probes.
package metrics

import (
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// QueryStatus represents the status of a DNS query.
type QueryStatus string

const (
	QuerySuccess QueryStatus = "success"
	QueryTimeout QueryStatus = "timeout"
	QueryError   QueryStatus = "error"
)

// ProbeMetrics holds all metrics for CoreDNS probe.
type ProbeMetrics struct {
	// Metrics for DNS queries per CoreDNS endpoint
	rttHistogram *prometheus.HistogramVec

	// Registry for all metrics
	registry *prometheus.Registry

	// HTTP server for exposing metrics
	server *http.Server
}

// New creates a new ProbeMetrics instance with registered Prometheus metrics.
func New() *ProbeMetrics {
	registry := prometheus.NewRegistry()

	rttHistogram := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "coredns_probe_rtt_milliseconds",
			Help:    "Histogram of round-trip time for DNS queries in milliseconds",
			Buckets: []float64{0.5, 1, 1.5, 2, 2.5, 3, 3.5, 4, 4.5, 5, 10, 20, 50, 100, 200, 500, 1000},
		},
		[]string{"endpoint", "status"},
	)

	registry.MustRegister(rttHistogram)

	return &ProbeMetrics{
		rttHistogram: rttHistogram,
		registry:     registry,
	}
}

// RecordQuery records statistics for a single DNS probe query.
func (p *ProbeMetrics) RecordQuery(endpoint string, status QueryStatus, rtt time.Duration) {
	p.rttHistogram.WithLabelValues(endpoint, string(status)).Observe(float64(rtt.Nanoseconds()) / 1e6)
}

// StartServer starts the HTTP server for Prometheus metrics on the given address.
// The server runs in a separate goroutine.
func (p *ProbeMetrics) StartServer(addr string) error {
	if p.server != nil {
		return nil
	}

	handler := p.GetHandler()

	p.server = &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Warning: Metrics server stopped unexpectedly: %v", err)
		}
	}()

	return nil
}

// GetHandler returns an HTTP handler for serving metrics,
// useful for testing or integration with existing HTTP servers.
func (p *ProbeMetrics) GetHandler() http.Handler {
	mux := http.NewServeMux()
	handler := promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
	mux.Handle("/metrics", handler)
	return mux
}
