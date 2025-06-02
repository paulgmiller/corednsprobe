// Package e2e contains end-to-end tests for the CoreDNS probe.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/prometheus/common/expfmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CoreDNS Probe E2E Suite")
}

const (
	clusterName    = "corednsprobe-test"
	namespace      = "kube-system"
	deploymentName = "coredns-probe"
	metricsPort    = 9091
	probeImage     = "paulgmiller/corednsprobe:e2etest"
)

var (
	clientset *kubernetes.Clientset
	testDir   string
)

var _ = BeforeSuite(func() {
	// Create a temporary directory for test artifacts.
	testDir, err := os.MkdirTemp("", "corednsprobe-e2e-")
	Expect(err).NotTo(HaveOccurred())

	By("Creating a Kind cluster")
	kubeConfigPath := filepath.Join(testDir, "kubeconfig")
	os.Setenv("KUBECONFIG", kubeConfigPath)
	kindCmd := exec.Command("kind", "create", "cluster", "--name", clusterName, "--kubeconfig", kubeConfigPath)
	output, err := kindCmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to create Kind cluster: %s", string(output))
	GinkgoWriter.Println(string(output))

	// Initialize Kubernetes client.
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	Expect(err).NotTo(HaveOccurred())
	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())

	By("Building Docker image for CoreDNS probe")
	gitRoot, err := getGitRoot()
	Expect(err).NotTo(HaveOccurred(), "Failed to get Git root directory")
	buildCmd := exec.Command("docker", "build", "-t", probeImage, gitRoot)
	buildOutput, err := buildCmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to build Docker image: %s", string(buildOutput))
	GinkgoWriter.Println(string(buildOutput))

	By("Loading Docker image into Kind")
	loadCmd := exec.Command("kind", "load", "docker-image", probeImage, "--name", clusterName)
	loadOutput, err := loadCmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to load image into Kind: %s", string(loadOutput))
	GinkgoWriter.Println(string(loadOutput))

	By("Waiting for CoreDNS pods to be running")
	Eventually(func() bool {
		podList, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "k8s-app=kube-dns",
		})
		if err != nil {
			return false
		}
		for _, pod := range podList.Items {
			if pod.Status.Phase != "Running" {
				return false
			}
		}
		return len(podList.Items) > 0
	}, "180s", "2s").Should(BeTrue(), "CoreDNS pods are not running")

	By("Deploying CoreDNS probe")
	deployCmdStr := fmt.Sprintf("kustomize edit set image %s && kustomize build . | kubectl apply -f -", probeImage)
	deployCmd := exec.Command("bash", "-c", deployCmdStr)
	deployCmd.Env = os.Environ()
	deployCmd.Dir = filepath.Join(gitRoot, "config", "overlays", "e2e")
	deployOutput, err := deployCmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to deploy CoreDNS probe: %s", string(deployOutput))
	GinkgoWriter.Println(string(deployOutput))

	By("Waiting for CoreDNS probe deployment to become ready")
	Eventually(func() bool {
		deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return deployment.Status.ReadyReplicas == *deployment.Spec.Replicas
	}, "90s", "2s").Should(BeTrue())

	By("Listing all pods in all namespaces")
	podsCmd := exec.Command("kubectl", "get", "po", "-A")
	podsCmd.Env = os.Environ()
	podsOutput, err := podsCmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "Failed to list pods: %s", string(podsOutput))
	GinkgoWriter.Println(string(podsOutput))
})

var _ = AfterSuite(func() {
	By("Deleting the Kind cluster")
	kindCmd := exec.Command("kind", "delete", "cluster", "--name", clusterName)
	kindCmd.CombinedOutput()

	os.RemoveAll(testDir)
})

var _ = Describe("CoreDNS Probe deployment", func() {
	It("should have the CoreDNS probe pod running", func() {
		deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), deploymentName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(deployment.Status.AvailableReplicas).To(Equal(*deployment.Spec.Replicas))

		podList, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app=" + deploymentName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(podList.Items).NotTo(BeEmpty())
	})

	It("should expose metrics endpoint", func() {
		podList, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app=" + deploymentName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(podList.Items).NotTo(BeEmpty())

		pod := podList.Items[0]

		By("Port-forwarding to the CoreDNS probe pod")
		portForwardCmd := exec.Command("kubectl", "port-forward",
			fmt.Sprintf("pod/%s", pod.Name),
			fmt.Sprintf("%d:%d", metricsPort, metricsPort),
			"-n", namespace)
		portForwardCmd.Env = os.Environ()
		session, err := gexec.Start(portForwardCmd, GinkgoWriter, GinkgoWriter)
		defer session.Kill()
		Expect(err).NotTo(HaveOccurred())

		By("Waiting for port forwarding to be established")
		Eventually(session, "5s", "1s").Should(gbytes.Say("Forwarding from"), "Failed to establish port-forwarding")

		By("Checking if metrics endpoint is accessible")
		res, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", metricsPort))
		defer res.Body.Close()
		Expect(err).NotTo(HaveOccurred(), "Failed to access metrics endpoint")
		Expect(res.StatusCode).To(Equal(http.StatusOK), "Metrics endpoint did not return 200 OK")

		By("Verifying metrics format")
		body, err := io.ReadAll(res.Body)
		Expect(err).NotTo(HaveOccurred(), "Failed to read response body")
		Expect(body).NotTo(BeEmpty(), "Metrics response body is empty")
		var parser expfmt.TextParser
		metrics, err := parser.TextToMetricFamilies(strings.NewReader(string(body)))
		Expect(err).NotTo(HaveOccurred(), "Failed to parse metrics")
		metric := metrics["coredns_probe_rtt_milliseconds"]
		Expect(metric).NotTo(BeNil(), "Expected coredns_probe_rtt_milliseconds metric not found")
	})
})

// getGitRoot retrieves the root directory of the Git repository.
func getGitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get Git root directory: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
