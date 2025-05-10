// coredns_probe_slices.go — v3: per‑server success‑rate & RTT
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	namespace       = "kube-system"
	serviceName     = "kube-dns"
	queryDomain     = "bing.com"
	queryTimeout    = 100 * time.Millisecond
	loopInterval    = 100 * time.Millisecond
	summaryInterval = 10 * time.Second
	sliceLabel      = discoveryv1.LabelServiceName
)

// per‑endpoint rolling stats (reset every summaryInterval)
type epStats struct {
	total    atomic.Int64 // total queries
	fail     atomic.Int64 // failures
	rttNanos atomic.Int64 // sum of RTT for successes
}

func main() {
	ctx := context.Background()
	client := mustClient()

	// --- discover CoreDNS pod IPs via EndpointSlices ---------------------------
	slices, err := client.DiscoveryV1().EndpointSlices(namespace).
		List(ctx, metav1.ListOptions{LabelSelector: sliceLabel + "=" + serviceName})
	if err != nil {
		log.Fatalf("listing EndpointSlices failed: %v", err)
	}

	var servers []string
	for _, es := range slices.Items {
		for _, ep := range es.Endpoints {
			for _, addr := range ep.Addresses {
				servers = append(servers, addr)
			}
		}
	}
	if len(servers) == 0 {
		log.Fatalf("no CoreDNS pod IPs found in EndpointSlices for %s/%s", namespace, serviceName)
	}
	log.Printf("found %d CoreDNS endpoints %v", len(servers), servers)

	// --- allocate stats slice aligned with servers -----------------------------
	stats := make([]*epStats, len(servers))
	for i := range stats {
		stats[i] = &epStats{}
	}

	// --- ticker loops ----------------------------------------------------------
	probeTicker := time.NewTicker(loopInterval)
	defer probeTicker.Stop()

	summaryTicker := time.NewTicker(summaryInterval)
	defer summaryTicker.Stop()

	for {
		select {
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
						return
					}
					st.rttNanos.Add(rtt.Nanoseconds())
				}(idx, ip)
			}
			wg.Wait()

		case <-summaryTicker.C:
			fmt.Println("[summary] last 10 s:")
			for i, ip := range servers {
				st := stats[i]
				total := st.total.Load() //this could get ahead of fail
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
					avgRTTms = fmt.Sprintf("%.2f ms", float64(sumRTT)/float64(ok)/1e6)
				}
				fmt.Printf("  %s → success %.1f %% (%d/%d)  avgRTT %s\n",
					ip, successPct, ok, total, avgRTTms)
			}
			fmt.Println()
		}
	}
}

// lookupThrough resolves queryDomain using addr:53 via net.Resolver.
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

// same helper as before --------------------------------------------------------
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
