// Package metrics provides Prometheus metrics for CoreDNS probes.
package metrics

import (
	"context"
	"errors"
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

	//hold the server so we can shutdown gracefully
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

// Start server will fork  a go routine. Failures result in program shutdown
func (p *ProbeMetrics) StartServer(ctx context.Context, addr string) {

	mux := http.NewServeMux()
	handler := promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
	mux.Handle("/metrics", handler)

	p.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	if err := p.server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen error: %v", err)
	}

}

func (p *ProbeMetrics) StopServer(ctx context.Context) {
	if p.server == nil {
		log.Fatalf("server is not running, cannot stop")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log.Println("Shutting down HTTP server ...")
	if err := p.server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
}
