// coredns_probe_slices.go — v3: per‑server success‑rate & RTT
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/paulgmiller/corednsprobe/pkg/metrics"
	v1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const sliceLabel = v1.LabelServiceName

// Config holds CLI and env settings
type Config struct {
	Namespace       string        `arg:"--namespace,env:NAMESPACE" default:"kube-system" help:"Kubernetes namespace"`
	ServiceName     string        `arg:"--service-name,env:SERVICE_NAME" default:"kube-dns" help:"Service name"`
	QueryDomain     string        `arg:"--query-domain,env:QUERY_DOMAIN" default:"bing.com" help:"Domain to query"`
	QueryTimeout    time.Duration `arg:"--query-timeout,env:QUERY_TIMEOUT" default:"100ms" help:"DNS query timeout"`
	LoopInterval    time.Duration `arg:"--loop-interval,env:LOOP_INTERVAL" default:"100ms" help:"Probe loop interval"`
	SummaryInterval time.Duration `arg:"--summary-interval,env:SUMMARY_INTERVAL" default:"10s" help:"Summary interval"`
	MetricsAddr     string        `arg:"--metrics-addr,env:METRICS_ADDR" default:":9091" help:"Address to expose Prometheus metrics"`
}

// global settings populated in main()
var (
	namespace       string
	serviceName     string
	queryDomain     string
	queryTimeout    time.Duration
	loopInterval    time.Duration
	summaryInterval time.Duration
	metricsAddr     string
)

func main() {
	var cfg Config
	arg.MustParse(&cfg)
	namespace, serviceName = cfg.Namespace, cfg.ServiceName
	queryDomain, queryTimeout = cfg.QueryDomain, cfg.QueryTimeout
	loopInterval, summaryInterval = cfg.LoopInterval, cfg.SummaryInterval
	metricsAddr = cfg.MetricsAddr

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize metrics
	probeMetrics := metrics.New()
	if err := probeMetrics.StartServer(ctx, metricsAddr); err != nil {
		log.Fatalf("Failed to start metrics server: %v", err)
	}
	log.Printf("Metrics server started on %s/metrics", metricsAddr)

	client := mustClient()

	slices, err := client.DiscoveryV1().EndpointSlices(namespace).
		List(ctx, metav1.ListOptions{LabelSelector: sliceLabel + "=" + serviceName})
	if err != nil {
		log.Fatalf("listing EndpointSlices failed: %v", err)
	}

	var servers []string
	for _, es := range slices.Items {
		for _, ep := range es.Endpoints {
			servers = append(servers, ep.Addresses...)
		}
	}
	if len(servers) == 0 {
		log.Fatalf("no CoreDNS pod IPs found in EndpointSlices for %s/%s", namespace, serviceName)
	}
	log.Printf("found %d CoreDNS endpoints %v", len(servers), servers)

	stats := make([]*epStats, len(servers))
	for i := range stats {
		stats[i] = &epStats{}
	}

	probeTicker := time.NewTicker(loopInterval)
	defer probeTicker.Stop()
	summaryTicker := time.NewTicker(summaryInterval)
	defer summaryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-probeTicker.C:
			var wg sync.WaitGroup
			for idx, ip := range servers {
				wg.Add(1)
				go func(i int, addr string) {
					defer wg.Done()
					st := stats[i]
					st.total.Add(1)

					rtt, err := lookupThrough(addr)
					if err != nil || rtt > queryTimeout {
						st.fail.Add(1)
						if errors.Is(err, context.DeadlineExceeded) {
							probeMetrics.RecordQuery(addr, metrics.QueryTimeout, rtt)
							return
						}

						probeMetrics.RecordQuery(addr, metrics.QueryError, rtt)
						return
					}

					probeMetrics.RecordQuery(addr, metrics.QuerySuccess, rtt)
					st.rttNanos.Add(rtt.Nanoseconds())
				}(idx, ip)
			}
			wg.Wait()

		case <-summaryTicker.C:
			fmt.Println("[summary] last 10 s:")
			for i, ip := range servers {
				st := stats[i]
				total := st.total.Load()
				fail := st.fail.Load()
				sumRTT := st.rttNanos.Load()

				if total == 0 {
					fmt.Printf("  %s → no queries\n", ip)
					continue
				}
				ok := total - fail
				successPct := float64(ok) / float64(total) * 100
				avgRTTms := "n/a"
				if ok > 0 {
					avgRTTms = fmt.Sprintf("%.2f ms", float64(sumRTT)/float64(ok)/1e6)
				}
				fmt.Printf("  %s → success %.1f %% (%d/%d)  avgRTT %s\n",
					ip, successPct, ok, total, avgRTTms)
			}
			fmt.Println()
		}
	}
}

type epStats struct {
	total    atomic.Int64 // total queries
	fail     atomic.Int64 // failures
	rttNanos atomic.Int64 // sum of RTT for successes
}

func lookupThrough(addr string) (time.Duration, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: queryTimeout}
			return d.DialContext(ctx, network, net.JoinHostPort(addr, "53"))
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	start := time.Now()
	_, err := resolver.LookupHost(ctx, queryDomain)
	return time.Since(start), err
}

func mustClient() *kubernetes.Clientset {
	if cfg, err := rest.InClusterConfig(); err == nil {
		cs, _ := kubernetes.NewForConfig(cfg)
		return cs
	}
	kubeCfg := os.Getenv("KUBECONFIG")
	if kubeCfg == "" {
		kubeCfg = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeCfg)
	if err != nil {
		log.Fatalf("loading kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("building clientset: %v", err)
	}
	return cs
}
