# CoreDNS Probe

CoreDNS Probe is a Go-based diagnostic tool designed to monitor the performance of CoreDNS pods in a Kubernetes cluster. It measures DNS query success rates and response times to help diagnose DNS-related issues in Kubernetes environments.

## Features

- **CoreDNS Endpoint Discovery**: Automatically discovers CoreDNS pod IPs through Kubernetes `EndpointSlices`.
- **Per-Endpoint Statistics**: Tracks rolling statistics for each CoreDNS pod:
  - Total queries
  - Number of failures
  - Aggregate round-trip time (RTT) for successful queries
- **Parallel Probing**: Sends DNS queries to all CoreDNS pods concurrently.
- **Summary Reporting**: Outputs success rates and average response times every 10 seconds.
- **Prometheus Metrics**: Exposes metrics in Prometheus format via a `/metrics` HTTP endpoint for monitoring.

## How It Works

1. The tool connects to the Kubernetes cluster using `kubeconfig` or in-cluster configuration.
1. It discovers CoreDNS pod IPs via `EndpointSlices` in the `kube-system` namespace for the `kube-dns` service.
1. Periodically sends DNS queries (`bing.com`) to each CoreDNS pod.
1. Collects and computes rolling statistics on query success and RTT.
1. Outputs a summary report every 10 seconds to the console.

## Build

1. Clone the repository:

   ```bash
   git clone https://github.com/paulgmiller/corednsprobe.git
   ```

1. Build the tool:

   ```bash
   cd corednsprobe
   go build -o corednsprobe main.go
   ```

## Deployment

For streamlined deployment, a `deploy.yaml` file is provided. This file can be applied directly to your Kubernetes cluster to deploy the CoreDNS Probe tool. The container image is hosted on Microsoft Container Registry (MCR), ensuring secure and efficient access.

### Steps to Deploy

1. Apply the deployment manifest:

   ```bash
   kubectl apply -f deploy.yaml
   ```

1. Monitor the deployment and ensure the pods are running:

   ```bash
   kubectl get pods -n <namespace>
   ```

## Usage

1. Set up the Kubernetes environment:
   - Ensure the tool can access your Kubernetes cluster either via in-cluster configuration or a valid `KUBECONFIG` environment variable.
1. Run the tool:

   ```bash
   ./corednsprobe
1. Monitor the output for DNS success rates and response times.

The tool will display statistics every 10 seconds, including:

- Success rate for DNS queries to each CoreDNS pod.
- Average Round-Trip Time (RTT) for successful queries.

## Example Output

```text
[summary] last 10 s:
  10.0.0.1 → success 98.0 % (490/500)  avgRTT 2.34 ms
  10.0.0.2 → success 99.0 % (495/500)  avgRTT 1.87 ms
```

## Configuration

The following variables can be changed with args or env vars in the container.

- `namespace`: Kubernetes namespace to search for CoreDNS pods (default: `kube-system`).
- `serviceName`: Kubernetes service name for CoreDNS (default: `kube-dns`).
- `queryDomain`: Domain used for DNS queries (default: `bing.com`).
- `queryTimeout`: Timeout for DNS queries (default: `100ms`).
- `loopInterval`: Interval between query loops (default: `100ms`).
- `summaryInterval`: Interval for summary reporting (default: `10s`).
- `metricsAddr`: Address to expose Prometheus metrics (default: `:9091`).

### Available Prometheus Metrics

The following metrics are available at the `/metrics` endpoint:

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `coredns_probe_rtt_milliseconds` | Histogram | `endpoint`, `status` | Histogram of round-trip time for DNS queries in milliseconds |

The `status` label has the following possible values:

- `success`: Query completed successfully
- `timeout`: Query timed out
- `error`: Query failed due to an error other than timeout

## License

This project is licensed under the [MIT License](LICENSE).
