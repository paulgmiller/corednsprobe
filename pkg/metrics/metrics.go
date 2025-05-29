// Package metrics provides Prometheus metrics for CoreDNS probes.
package metrics

import (
	"context"
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

var rttHistogram = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "coredns_probe_rtt_milliseconds",
		Help:    "Histogram of round-trip time for DNS queries in milliseconds",
		Buckets: []float64{0.5, 1, 1.5, 2, 2.5, 3, 3.5, 4, 4.5, 5, 10, 20, 50, 100, 200, 500, 1000},
	},
	[]string{"endpoint", "status"},
)

// RecordQuery records statistics for a single DNS probe query.
func RecordQuery(endpoint string, status QueryStatus, rtt time.Duration) {
	rttHistogram.WithLabelValues(endpoint, string(status)).Observe(float64(rtt.Nanoseconds()) / 1e6)
}

// this does not block so we will not shutdown gracefully
func StartServer(ctx context.Context, addr string) {
	prometheus.MustRegister(rttHistogram)
	http.Handle("/metrics", promhttp.Handler()) // uses the default registry

	go func() {
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()
}
