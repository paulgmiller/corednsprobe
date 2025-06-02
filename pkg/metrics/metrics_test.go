package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// DNSQuery represents a DNS query for testing.
type DNSQuery struct {
	status QueryStatus
	rtt    time.Duration
}

func TestRecordQuery(t *testing.T) {
	testCases := []struct {
		name                 string
		endpoint             string
		queries              []DNSQuery
		expectedSuccessRtt   float64
		expectedTimeoutRtt   float64
		expectedErrorRtt     float64
		expectedSuccessCount uint64
		expectedTimeoutCount uint64
		expectedErrorCount   uint64
	}{
		{
			name:     "successful_query",
			endpoint: "10.0.0.1",
			queries: []DNSQuery{
				{status: QuerySuccess, rtt: 10000000 * time.Nanosecond},
			},
			expectedSuccessRtt:   10.0,
			expectedSuccessCount: 1,
		},
		{
			name:     "timeout_query",
			endpoint: "10.0.0.2",
			queries: []DNSQuery{
				{status: QueryTimeout, rtt: 100000000 * time.Nanosecond},
			},
			expectedTimeoutRtt:   100.0,
			expectedTimeoutCount: 1,
		},
		{
			name:     "error_query",
			endpoint: "10.0.0.3",
			queries: []DNSQuery{
				{status: QueryError, rtt: 50000000 * time.Nanosecond},
			},
			expectedErrorRtt:   50.0,
			expectedErrorCount: 1,
		},
		{
			name:     "multiple_queries_with_mixed_statuses",
			endpoint: "10.0.0.4",
			queries: []DNSQuery{
				{status: QuerySuccess, rtt: 2150000 * time.Nanosecond},
				{status: QuerySuccess, rtt: 2430000 * time.Nanosecond},
				{status: QueryTimeout, rtt: 120000000 * time.Nanosecond},
				{status: QueryError, rtt: 45000000 * time.Nanosecond},
			},
			expectedSuccessRtt:   4.58,
			expectedTimeoutRtt:   120.0,
			expectedErrorRtt:     45.0,
			expectedSuccessCount: 2,
			expectedTimeoutCount: 1,
			expectedErrorCount:   1,
		},
		{
			name:     "minimal_rtt",
			endpoint: "10.0.0.5",
			queries: []DNSQuery{
				{status: QuerySuccess, rtt: 10000 * time.Nanosecond},
			},
			expectedSuccessRtt:   0.01,
			expectedSuccessCount: 1,
		},
		{
			name:     "high_rtt",
			endpoint: "10.0.0.6",
			queries: []DNSQuery{
				{status: QuerySuccess, rtt: 1000000000 * time.Nanosecond},
			},
			expectedSuccessRtt:   1000.0,
			expectedSuccessCount: 1,
		},
	}

	for _, tc := range testCases {
		for _, q := range tc.queries {
			RecordQuery(tc.endpoint, q.status, q.rtt)
		}
	}

	metricFamilies := setupAndFetchMetrics(t)
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.expectedTimeoutCount > 0 {
				verifyHistogram(t, metricFamilies, "coredns_probe_rtt_milliseconds",
					tc.endpoint, string(QueryTimeout), tc.expectedTimeoutRtt, tc.expectedTimeoutCount)
			} else {
				verifyHistogramNotExists(t, metricFamilies, "coredns_probe_rtt_milliseconds", tc.endpoint, string(QueryTimeout))
			}

			if tc.expectedErrorCount > 0 {
				verifyHistogram(t, metricFamilies, "coredns_probe_rtt_milliseconds",
					tc.endpoint, string(QueryError), tc.expectedErrorRtt, tc.expectedErrorCount)
			} else {
				verifyHistogramNotExists(t, metricFamilies, "coredns_probe_rtt_milliseconds", tc.endpoint, string(QueryError))
			}

			if tc.expectedSuccessCount > 0 {
				verifyHistogram(t, metricFamilies, "coredns_probe_rtt_milliseconds",
					tc.endpoint, string(QuerySuccess), tc.expectedSuccessRtt, tc.expectedSuccessCount)
			} else {
				verifyHistogramNotExists(t, metricFamilies, "coredns_probe_rtt_milliseconds", tc.endpoint, string(QuerySuccess))
			}
		})
	}
}

// setupAndFetchMetrics creates a test HTTP server with Prometheus metrics handler
// and returns the parsed metrics from a GET /metrics request.
func setupAndFetchMetrics(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()

	prometheus.MustRegister(rttHistogram)
	server := httptest.NewServer(promhttp.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("Failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200 OK, got %v", resp.Status)
	}

	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		t.Fatalf("Failed to parse metrics: %v", err)
	}

	return metricFamilies
}

// verifyHistogram checks that a histogram metric exists with the expected sum and count.
func verifyHistogram(t *testing.T, families map[string]*dto.MetricFamily, metricName, endpoint, status string,
	expectedSum float64, expectedCount uint64) {
	t.Helper()

	family, exists := families[metricName]
	if !exists {
		t.Fatalf("Metric %s not found", metricName)
	}

	if family.GetType() != dto.MetricType_HISTOGRAM {
		t.Fatalf("Expected histogram type for %s, got %v", metricName, family.GetType())
	}

	var histogram *dto.Histogram
	found := false
	for _, m := range family.Metric {
		if hasLabel(m, "endpoint", endpoint) && hasLabel(m, "status", status) {
			histogram = m.GetHistogram()
			if histogram == nil {
				t.Fatalf("Histogram data missing for %s with endpoint=%s, status=%s", metricName, endpoint, status)
			}
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("No metric %s found with endpoint=%s, status=%s", metricName, endpoint, status)
	}

	actualSum := histogram.GetSampleSum()
	if !(math.Abs(actualSum-expectedSum) < 0.01) {
		t.Errorf("%s sum for %s: expected %.2f, got %.2f", metricName, status, expectedSum, actualSum)
	}

	actualCount := histogram.GetSampleCount()
	if actualCount != expectedCount {
		t.Errorf("%s sample count for %s: expected %d, got %d", metricName, status, expectedCount, actualCount)
	}

	// For non-zero RTTs, check that at least one bucket has been incremented.
	if expectedSum > 0 {
		foundIncrement := false
		for _, bucket := range histogram.GetBucket() {
			if bucket.GetCumulativeCount() > 0 {
				foundIncrement = true
				break
			}
		}
		if !foundIncrement {
			t.Errorf("%s has positive sum but all buckets are zero", metricName)
		}
	}
}

// verifyHistogramNotExists checks that a histogram metric doesn't exist for a specific endpoint and status label.
func verifyHistogramNotExists(t *testing.T, families map[string]*dto.MetricFamily, metricName, endpoint, status string) {
	t.Helper()

	family, exists := families[metricName]
	if !exists {
		return
	}

	for _, m := range family.Metric {
		if hasLabel(m, "endpoint", endpoint) && hasLabel(m, "status", status) {
			t.Errorf("Histogram metric %s unexpectedly exists with endpoint=%s, status=%s", metricName, endpoint, status)
			return
		}
	}
}

// hasLabel checks if a metric has a label with the given name and value.
func hasLabel(metric *dto.Metric, name, value string) bool {
	for _, label := range metric.Label {
		if label.GetName() == name && label.GetValue() == value {
			return true
		}
	}
	return false
}
