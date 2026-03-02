package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/hosting-panel/panel-core/internal/keycloak"
	"github.com/hosting-panel/panel-core/internal/middleware"
	"github.com/hosting-panel/panel-core/internal/powerdns"
)

// pdnsZoneAdapter adapts powerdns.Client to the PDNSZoneCreator interface.
type pdnsZoneAdapter struct {
	client *powerdns.Client
}

func (a *pdnsZoneAdapter) CreateZone(ctx context.Context, name string, nameservers []string) error {
	zone := powerdns.Zone{
		Name:    name,
		Kind:    "Native",
		DNSSec:  false,
	}
	// Add NS records as nameservers
	if len(nameservers) > 0 {
		var records []powerdns.Record
		for _, ns := range nameservers {
			records = append(records, powerdns.Record{Content: ns, Disabled: false})
		}
		zone.RRSets = []powerdns.RRSet{
			{Name: name, Type: "NS", TTL: 3600, Records: records},
		}
	}
	_, err := a.client.CreateZone(ctx, zone)
	return err
}

func (a *pdnsZoneAdapter) PatchRRSets(ctx context.Context, zoneID string, rrsets interface{}) error {
	patch, ok := rrsets.(powerdns.ZonePatch)
	if !ok {
		return fmt.Errorf("invalid rrsets type")
	}
	return a.client.PatchZone(ctx, zoneID, patch)
}

// routerConfig holds optional dependencies for the router.
type routerConfig struct {
	dynClient        dynamic.Interface
	pdnsClient       *powerdns.Client
	externalIP       string
	mailHost         string
	clusterIssuer    string
	restConfig       *rest.Config
	prometheusURL    string
	platformVersion  string
	helmChartVersion string
}

// RouterOption configures the router with optional dependencies.
type RouterOption func(*routerConfig)

// WithDynamicClient sets the dynamic Kubernetes client for CRD operations.
func WithDynamicClient(dc dynamic.Interface) RouterOption {
	return func(cfg *routerConfig) {
		cfg.dynClient = dc
	}
}

// WithPowerDNS sets the PowerDNS client and related config for DNS management.
func WithPowerDNS(pdns *powerdns.Client, externalIP, mailHost string) RouterOption {
	return func(cfg *routerConfig) {
		cfg.pdnsClient = pdns
		cfg.externalIP = externalIP
		cfg.mailHost = mailHost
	}
}

// WithClusterIssuer sets the default ClusterIssuer for certificate management.
func WithClusterIssuer(issuer string) RouterOption {
	return func(cfg *routerConfig) {
		cfg.clusterIssuer = issuer
	}
}

// WithRESTConfig sets the Kubernetes REST config for pod exec operations.
func WithRESTConfig(rc *rest.Config) RouterOption {
	return func(cfg *routerConfig) {
		cfg.restConfig = rc
	}
}

// WithPrometheusURL sets the internal Prometheus URL for metrics proxying.
func WithPrometheusURL(url string) RouterOption {
	return func(cfg *routerConfig) {
		cfg.prometheusURL = url
	}
}

// WithVersionInfo sets the platform and Helm chart version strings.
func WithVersionInfo(platformVersion, helmChartVersion string) RouterOption {
	return func(cfg *routerConfig) {
		cfg.platformVersion = platformVersion
		cfg.helmChartVersion = helmChartVersion
	}
}

// NewRouter creates and configures the chi router with all middleware and routes.
func NewRouter(clientset kubernetes.Interface, keycloakAdmin *keycloak.AdminClient, opts ...RouterOption) http.Handler {
	cfg := &routerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	r := chi.NewRouter()

	// Global middleware stack
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(middleware.RequestLogger)
	r.Use(chimw.Recoverer)

	// Prometheus metrics endpoint (unauthenticated)
	r.Handle("/metrics", promhttp.Handler())

	// Health endpoint (unauthenticated)
	r.Get("/api/health", HealthHandler(clientset))

	// Auth endpoints (unauthenticated — before OIDC middleware)
	authHandler := NewAuthHandler()
	if authHandler != nil {
		r.Get("/api/auth/login", authHandler.LoginHandler())
		r.Get("/api/auth/callback", authHandler.CallbackHandler())
		r.Post("/api/auth/logout", authHandler.LogoutHandler())
		r.Post("/api/auth/refresh", authHandler.RefreshHandler())
	}

	// Authenticated API routes
	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.OIDCAuth())

		// User management routes — mixed admin-only and self-access
		r.Route("/users", func(r chi.Router) {
			userHandler := NewUserHandler(keycloakAdmin, clientset, cfg.dynClient)
			userHandler.RegisterRoutes(r)
		})

		// Routes accessible by both admin and user roles:
		r.Route("/websites", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				websiteHandler := NewWebsiteHandler(cfg.dynClient, clientset)
				if cfg.pdnsClient != nil && cfg.pdnsClient.Configured() {
					websiteHandler.WithDNS(&pdnsZoneAdapter{client: cfg.pdnsClient}, cfg.externalIP, cfg.mailHost)
				}
				websiteHandler.RegisterRoutes(r)
			}
		})

		r.Route("/databases", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				databaseHandler := NewDatabaseHandler(cfg.dynClient, clientset)
				databaseHandler.RegisterRoutes(r)
			}
		})

		r.Route("/email-accounts", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				emailHandler := NewEmailHandler(cfg.dynClient, clientset, keycloakAdmin)
				emailHandler.RegisterRoutes(r)
			}
		})

		r.Route("/email-domains", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				emailDomainHandler := NewEmailDomainHandler(cfg.dynClient)
				emailDomainHandler.RegisterRoutes(r)
			}
		})

		r.Route("/dns", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.pdnsClient != nil && cfg.pdnsClient.Configured() {
				dnsHandler := NewDNSHandler(cfg.pdnsClient, cfg.externalIP, cfg.mailHost)
				dnsHandler.RegisterRoutes(r)
			}
		})

		r.Route("/certificates", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				certHandler := NewCertificateHandler(cfg.dynClient, clientset, cfg.clusterIssuer)
				certHandler.RegisterRoutes(r)
			}
		})

		r.Route("/backups", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil {
				backupHandler := NewBackupHandler(cfg.dynClient)
				backupHandler.RegisterRoutes(r)
				// VestaCP/HestiaCP import — admin only, handled inside
				importHandler := NewImportHandler(cfg.dynClient)
				r.Post("/import", importHandler.HandleImport)
			}
		})

		r.Route("/hosting-plans", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))
			if cfg.dynClient != nil {
				hostingPlanHandler := NewHostingPlanHandler(cfg.dynClient, clientset)
				hostingPlanHandler.RegisterRoutes(r)
			}
		})

		r.Route("/php-profiles", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			phpProfileHandler := NewPHPProfileHandler(clientset)
			phpProfileHandler.RegisterRoutes(r)
		})

		r.Route("/files", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			if cfg.dynClient != nil && cfg.restConfig != nil {
				fileHandler := NewFileManagerHandler(cfg.dynClient, clientset, cfg.restConfig)
				fileHandler.RegisterRoutes(r)
			}
		})

		r.Route("/cron-jobs", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			cronJobHandler := NewCronJobHandler(clientset, cfg.dynClient)
			cronJobHandler.RegisterRoutes(r)
		})

		r.Route("/firewall", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))
			firewallHandler := NewFirewallHandler(clientset)
			firewallHandler.RegisterRoutes(r)
		})

		r.Route("/monitoring", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin"))
			monitoringHandler := NewMonitoringHandler(clientset, cfg.prometheusURL)
			monitoringHandler.RegisterRoutes(r)
		})

		r.Route("/settings", func(r chi.Router) {
			r.Use(middleware.RequireRole("admin", "user"))
			settingsHandler := NewSettingsHandler(clientset, cfg.platformVersion, cfg.helmChartVersion)
			settingsHandler.RegisterRoutes(r)
		})
	})

	return r
}
