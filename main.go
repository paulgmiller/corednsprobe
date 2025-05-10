// coredns_probe_slices.go
package main

import (
	"context"
	"log"
	"net"
	"os"
	"sync"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	namespace        = "kube-system"
	serviceName      = "kube-dns"          // most clusters still call the service “kube-dns”
	queryDomain      = "bing.com"
	queryTimeout     = 100 * time.Millisecond
	loopInterval     = 100 * time.Millisecond
	sliceLabel       = discoveryv1.LabelServiceName // = "kubernetes.io/service-name"
)

func main() {
	ctx := context.Background()
	client := mustClient()

	// ---------------------------------------------------------------------
	// Discover CoreDNS pod IPs from EndpointSlices
	// ---------------------------------------------------------------------
	slices, err := client.DiscoveryV1().
		EndpointSlices(namespace).
		List(ctx, metav1.ListOptions{LabelSelector: sliceLabel + "=" + serviceName})
	if err != nil {
		log.Fatalf("listing EndpointSlices failed: %v", err)
	}
	var servers []string
	for _, es := range slices.Items {
		for _, ep := range es.Endpoints {
			for _, addr := range ep.Addresses {
				servers = append(servers, addr) // plain IP (port fixed to 53 later)
			}
		}
	}
	if len(servers) == 0 {
		log.Fatalf("no CoreDNS pod IPs found in EndpointSlices for %s/%s", namespace, serviceName)
	}
	log.Printf("found %d CoreDNS endpoints: %v", len(servers), servers)

	// ---------------------------------------------------------------------
	// Prepare loop → every 0.1 s resolve bing.com through each IP
	// ---------------------------------------------------------------------
	ticker := time.NewTicker(loopInterval)
	defer ticker.Stop()

	for ts := range ticker.C {
		var wg sync.WaitGroup
		for _, ip := range servers {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				rtt, err := lookupThrough(addr)
				if err != nil || rtt > queryTimeout {
					log.Printf("[%s] FAIL via %s (rtt=%v, err=%v)",
						ts.Format(time.RFC3339Nano), addr, rtt, err)
				} else {
					log.Printf("[%s] OK   via %s (rtt=%v)",
						ts.Format(time.RFC3339Nano), addr, rtt)
				}
			}(ip)
		}
		wg.Wait()
	}
}

// lookupThrough resolves queryDomain using addr:53 with the std‑lib resolver.
func lookupThrough(addr string) (time.Duration, error) {
	resolver := &net.Resolver{
		PreferGo: true, // use Go's built‑in DNS instead of libc so we can override Dial
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: queryTimeout}
			return d.DialContext(ctx, network, net.JoinHostPort(addr, "53"))
		},
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	_, err := resolver.LookupHost(ctx, queryDomain)
	return time.Since(start), err
}

// mustClient builds an in‑cluster client first, else falls back to KUBECONFIG.
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
