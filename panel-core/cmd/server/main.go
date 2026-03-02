package main

import (
	"log"
	"net/http"
	"os"

	"github.com/hosting-panel/panel-core/internal/api"
	"github.com/hosting-panel/panel-core/internal/k8s"
	"github.com/hosting-panel/panel-core/internal/keycloak"
	"github.com/hosting-panel/panel-core/internal/powerdns"
	"k8s.io/client-go/rest"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Initialize Kubernetes client
	clientset, err := k8s.NewClientset()
	if err != nil {
		log.Printf("WARNING: Kubernetes client initialization failed: %v", err)
		log.Printf("Health endpoint will report k8s as unavailable")
		// We still start the server — the health endpoint will report degraded status.
		// This allows the panel to start even outside a k8s cluster during development.
	}

	// Initialize Keycloak Admin API client
	keycloakAdmin := keycloak.NewAdminClient()
	if !keycloakAdmin.Configured() {
		log.Printf("WARNING: Keycloak Admin API not configured — user management will fail")
	}

	// Initialize dynamic Kubernetes client for CRD operations
	dynClient, err := k8s.NewDynamicClient()
	if err != nil {
		log.Printf("WARNING: Kubernetes dynamic client initialization failed: %v", err)
		log.Printf("CRD-based operations (websites, databases, etc.) will not work")
	}

	var routerOpts []api.RouterOption
	if dynClient != nil {
		routerOpts = append(routerOpts, api.WithDynamicClient(dynClient))
	}

	// Initialize PowerDNS client
	pdnsURL := os.Getenv("POWERDNS_API_URL")
	pdnsKey := os.Getenv("POWERDNS_API_KEY")
	pdnsClient := powerdns.NewClient(pdnsURL, pdnsKey)
	if pdnsClient.Configured() {
		externalIP := os.Getenv("PANEL_EXTERNAL_IP")
		mailHost := os.Getenv("PANEL_MAIL_HOST")
		if externalIP == "" {
			externalIP = "127.0.0.1" // default fallback IP
		}
		if mailHost == "" {
			mailHost = os.Getenv("PANEL_HOSTNAME")
		}
		routerOpts = append(routerOpts, api.WithPowerDNS(pdnsClient, externalIP, mailHost))
		log.Printf("PowerDNS: configured with URL %s", pdnsURL)
	} else {
		log.Printf("WARNING: PowerDNS not configured — DNS management will not work")
	}

	// Set default ClusterIssuer for cert-manager
	clusterIssuer := os.Getenv("CLUSTER_ISSUER")
	if clusterIssuer == "" {
		clusterIssuer = "letsencrypt-production"
	}
	routerOpts = append(routerOpts, api.WithClusterIssuer(clusterIssuer))

	// Initialize REST config for pod exec operations (File Manager)
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("WARNING: REST config initialization failed: %v — File Manager will not work", err)
	} else {
		routerOpts = append(routerOpts, api.WithRESTConfig(restConfig))
	}

	// Set Prometheus URL for monitoring proxy
	prometheusURL := os.Getenv("PROMETHEUS_URL")
	if prometheusURL == "" {
		prometheusURL = "http://prometheus-server.monitoring.svc.cluster.local:80"
	}
	routerOpts = append(routerOpts, api.WithPrometheusURL(prometheusURL))

	// Set version info for settings endpoint
	platformVersion := os.Getenv("PLATFORM_VERSION")
	helmChartVersion := os.Getenv("HELM_CHART_VERSION")
	if platformVersion == "" {
		platformVersion = "1.0.0"
	}
	if helmChartVersion == "" {
		helmChartVersion = "0.1.0"
	}
	routerOpts = append(routerOpts, api.WithVersionInfo(platformVersion, helmChartVersion))

	router := api.NewRouter(clientset, keycloakAdmin, routerOpts...)

	buildVersion := os.Getenv("BUILD_VERSION")
	if buildVersion == "" {
		buildVersion = "dev"
	}
	log.Printf("Panel Core v%s starting on :%s", buildVersion, port)
	if err := http.ListenAndServe(":"+port, router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
