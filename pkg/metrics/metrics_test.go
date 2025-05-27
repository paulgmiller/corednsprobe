package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// DNSQuery represents a DNS query for testing.
type DNSQuery struct {
	isSuccess bool
	rtt       time.Duration
}

func TestProbeMetrics_RecordQuery(t *testing.T) {
	testCases := []struct {
		name           string
		endpoint       string
		queries        []DNSQuery
		expectedTotal  uint64
		expectedFailed uint64
		expectedRttMs  float64
	}{
		{
			name:     "successful_query",
			endpoint: "10.0.0.1",
			queries: []DNSQuery{
				{isSuccess: true, rtt: 10000000 * time.Nanosecond},
			},
			expectedTotal:  1,
			expectedFailed: 0,
			expectedRttMs:  10.0,
		},
		{
			name:     "failed_query",
			endpoint: "10.0.0.2",
			queries: []DNSQuery{
				{isSuccess: false, rtt: 100000000 * time.Nanosecond},
			},
			expectedTotal:  1,
			expectedFailed: 1,
			expectedRttMs:  0.0,
		},
		{
			name:     "multiple_queries",
			endpoint: "10.0.0.3",
			queries: []DNSQuery{
				{isSuccess: true, rtt: 2150000 * time.Nanosecond},
				{isSuccess: true, rtt: 2430000 * time.Nanosecond},
			},
			expectedTotal:  2,
			expectedFailed: 0,
			expectedRttMs:  4.58,
		},
		{
			name:     "minimal_rtt",
			endpoint: "10.0.0.4",
			queries: []DNSQuery{
				{isSuccess: true, rtt: 10000 * time.Nanosecond},
			},
			expectedTotal:  1,
			expectedFailed: 0,
			expectedRttMs:  0.01,
		},
		{
			name:     "high_rtt",
			endpoint: "10.0.0.5",
			queries: []DNSQuery{
				{isSuccess: true, rtt: 1000000000 * time.Nanosecond},
			},
			expectedTotal:  1,
			expectedFailed: 0,
			expectedRttMs:  1000.0,
		},
		{
			name:     "mixed_success_failure",
			endpoint: "10.0.0.6",
			queries: []DNSQuery{
				{isSuccess: true, rtt: 5000000 * time.Nanosecond},
				{isSuccess: false, rtt: 25000000 * time.Nanosecond},
			},
			expectedTotal:  2,
			expectedFailed: 1,
			expectedRttMs:  5.0,
		},
	}

	metrics := New()
	for _, tc := range testCases {
		for _, q := range tc.queries {
			metrics.RecordQuery(tc.endpoint, q.isSuccess, q.rtt)
		}
	}

	metricFamilies := setupAndFetchMetrics(t, metrics)
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			verifyCounter(t, metricFamilies, "coredns_probe_queries_total", tc.endpoint, tc.expectedTotal)

			if tc.expectedFailed > 0 {
				verifyCounter(t, metricFamilies, "coredns_probe_queries_failed_total", tc.endpoint, tc.expectedFailed)
			} else {
				verifyMetricNotExists(t, metricFamilies, "coredns_probe_queries_failed_total", tc.endpoint)
			}

			hasSuccessfulQueries := false
			for _, q := range tc.queries {
				if q.isSuccess {
					hasSuccessfulQueries = true
					break
				}
			}

			if hasSuccessfulQueries {
				verifyHistogramSum(t, metricFamilies, "coredns_probe_rtt_milliseconds", tc.endpoint, tc.expectedRttMs)
			} else {
				verifyMetricNotExists(t, metricFamilies, "coredns_probe_rtt_milliseconds", tc.endpoint)
			}
		})
	}
}

func TestProbeMetrics_ServerOperations(t *testing.T) {
	metrics := New()
	addr := ":8082"

	err := metrics.StartServer(addr)
	if err != nil {
		t.Fatalf("Failed to start metrics server: %v", err)
	}

	err = metrics.StartServer(addr)
	if err != nil {
		t.Errorf("Starting server again should be a no-op, got error: %v", err)
	}

	err = metrics.StopServer(5 * time.Second)
	if err != nil {
		t.Errorf("Failed to stop metrics server: %v", err)
	}

	err = metrics.StopServer(5 * time.Second)
	if err != nil {
		t.Errorf("Stopping server again should be a no-op, got error: %v", err)
	}
}

// setupAndFetchMetrics creates a test HTTP server with the provided metrics handler
// and returns the parsed metrics from a GET /metrics request.
func setupAndFetchMetrics(t *testing.T, metrics *ProbeMetrics) map[string]*dto.MetricFamily {
	t.Helper()

	handler := metrics.GetHandler()
	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics")
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

// verifyCounter checks that a counter metric exists with the expected value.
func verifyCounter(t *testing.T, families map[string]*dto.MetricFamily, metricName, labelValue string, expected uint64) {
	t.Helper()

	m := verifyMetricCommon(t, families, metricName, labelValue, dto.MetricType_COUNTER)

	counter := m.GetCounter()
	if counter == nil {
		t.Fatalf("Counter data missing for %s", metricName)
	}

	actual := uint64(counter.GetValue())
	if actual != expected {
		t.Errorf("%s: expected %d, got %d", metricName, expected, actual)
	}
}

// verifyHistogramSum checks that a histogram metric exists with the expected sum.
func verifyHistogramSum(t *testing.T, families map[string]*dto.MetricFamily, metricName, labelValue string, expected float64) {
	t.Helper()

	m := verifyMetricCommon(t, families, metricName, labelValue, dto.MetricType_HISTOGRAM)

	histogram := m.GetHistogram()
	if histogram == nil {
		t.Fatalf("Histogram data missing for %s", metricName)
	}

	actual := histogram.GetSampleSum()
	if !(math.Abs(actual-expected) < 0.01) {
		t.Errorf("%s sum: expected %.2f, got %.2f", metricName, expected, actual)
	}

	if expected > 0 && histogram.GetSampleCount() == 0 {
		t.Errorf("%s has positive sum but zero samples", metricName)
	}

	// For non-zero RTTs, check that at least one bucket has been incremented.
	if expected > 0 {
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

// verifyMetricNotExists checks that a metric doesn't exist for a specific endpoint label.
func verifyMetricNotExists(t *testing.T, families map[string]*dto.MetricFamily, metricName, endpointValue string) {
	t.Helper()

	family, exists := families[metricName]
	if !exists {
		return
	}

	for _, m := range family.Metric {
		if hasLabel(m, "endpoint", endpointValue) {
			t.Errorf("Metric %s unexpectedly exists with endpoint=%s", metricName, endpointValue)
			return
		}
	}
}

// verifyMetricCommon performs common validation for metrics and returns the matched metric.
func verifyMetricCommon(t *testing.T, families map[string]*dto.MetricFamily, metricName, labelValue string,
	expectedType dto.MetricType) *dto.Metric {

	t.Helper()
	family, exists := families[metricName]
	if !exists {
		t.Fatalf("Metric %s not found", metricName)
	}

	if family.GetType() != expectedType {
		t.Errorf("Expected %v type for %s, got %v", expectedType, metricName, family.GetType())
	}

	for _, m := range family.Metric {
		if hasLabel(m, "endpoint", labelValue) {
			return m
		}
	}

	t.Fatalf("No metric %s found with endpoint=%s", metricName, labelValue)
	return nil
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
