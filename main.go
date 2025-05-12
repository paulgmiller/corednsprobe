// coredns_probe_slices.go — v3: per‑server success‑rate & RTT
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const sliceLabel = discoveryv1.LabelServiceName

// Config holds CLI and env settings
type Config struct {
	Namespace       string        `mapstructure:"namespace"`
	ServiceName     string        `mapstructure:"service-name"`
	QueryDomain     string        `mapstructure:"query-domain"`
	QueryTimeout    time.Duration `mapstructure:"query-timeout"`
	LoopInterval    time.Duration `mapstructure:"loop-interval"`
	SummaryInterval time.Duration `mapstructure:"summary-interval"`
}

// global settings populated in RunE
var (
	namespace       string
	serviceName     string
	queryDomain     string
	queryTimeout    time.Duration
	loopInterval    time.Duration
	summaryInterval time.Duration
)

var rootCmd = &cobra.Command{
	Use:   "corednsprobe",
	Short: "CoreDNS probing tool",
	RunE: func(cmd *cobra.Command, args []string) error {
		// load all settings at once
		var cfg Config
		if err := viper.Unmarshal(&cfg); err != nil {
			return err
		}
		namespace, serviceName = cfg.Namespace, cfg.ServiceName
		queryDomain, queryTimeout = cfg.QueryDomain, cfg.QueryTimeout
		loopInterval, summaryInterval = cfg.LoopInterval, cfg.SummaryInterval

		ctx := context.Background()
		client := mustClient()

		slices, err := client.DiscoveryV1().EndpointSlices(namespace).
			List(ctx, metav1.ListOptions{LabelSelector: sliceLabel + "=" + serviceName})
		if err != nil {
			return fmt.Errorf("listing EndpointSlices failed: %w", err)
		}

		var servers []string
		for _, es := range slices.Items {
			for _, ep := range es.Endpoints {
				servers = append(servers, ep.Addresses...)
			}
		}
		if len(servers) == 0 {
			return fmt.Errorf("no CoreDNS pod IPs found in EndpointSlices for %s/%s", namespace, serviceName)
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
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().String("namespace", "kube-system", "Kubernetes namespace")
	rootCmd.PersistentFlags().String("service-name", "kube-dns", "Service name")
	rootCmd.PersistentFlags().String("query-domain", "bing.com", "Domain to query")
	rootCmd.PersistentFlags().Duration("query-timeout", 100*time.Millisecond, "DNS query timeout")
	rootCmd.PersistentFlags().Duration("loop-interval", 100*time.Millisecond, "Probe loop interval")
	rootCmd.PersistentFlags().Duration("summary-interval", 10*time.Second, "Summary interval")

	// bind all flags to viper
	viper.BindPFlags(rootCmd.PersistentFlags())
}

func initConfig() {
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
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

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
