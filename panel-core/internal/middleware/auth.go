package middleware

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// writeMiddlewareError writes a structured JSON error response without importing the api package
// (to avoid circular imports between middleware and api).
func writeMiddlewareError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

// contextKey is an unexported type for context keys in this package.
type contextKey string

const (
	// ClaimsContextKey is the context key for storing token claims.
	ClaimsContextKey contextKey = "claims"
)

// TokenClaims holds the relevant claims extracted from the OIDC token.
type TokenClaims struct {
	Subject  string   `json:"sub"`
	Email    string   `json:"email"`
	Username string   `json:"preferred_username"`
	Roles    []string `json:"-"`
}

// keycloakRealmAccess represents the realm_access claim structure from Keycloak tokens.
type keycloakRealmAccess struct {
	Roles []string `json:"roles"`
}

// rawClaims is used to unmarshal the full token claims including Keycloak-specific fields.
type rawClaims struct {
	Subject     string              `json:"sub"`
	Email       string              `json:"email"`
	Username    string              `json:"preferred_username"`
	RealmAccess keycloakRealmAccess `json:"realm_access"`
}

// OIDCAuth returns middleware that validates OIDC Bearer tokens from Keycloak.
// Configuration is read from environment variables:
//   - KEYCLOAK_ISSUER_URL: The OIDC issuer URL (e.g., https://keycloak.example.com/realms/hosting)
//     OR KEYCLOAK_URL + KEYCLOAK_REALM (e.g., http://keycloak-svc:80 + hosting)
//   - KEYCLOAK_CLIENT_ID: The OIDC client ID for token audience validation
func OIDCAuth() func(http.Handler) http.Handler {
	issuerURL := os.Getenv("KEYCLOAK_ISSUER_URL")
	clientID := os.Getenv("KEYCLOAK_CLIENT_ID")

	// discoveryURL is the internal URL used to fetch the OIDC discovery document.
	// externalIssuer is the issuer URL that Keycloak returns in the discovery document
	// (the external URL that tokens are signed with).
	discoveryURL := issuerURL
	externalIssuer := ""

	// Fall back to KEYCLOAK_URL + KEYCLOAK_REALM if KEYCLOAK_ISSUER_URL is not set
	if issuerURL == "" {
		kcURL := strings.TrimRight(os.Getenv("KEYCLOAK_URL"), "/")
		realm := os.Getenv("KEYCLOAK_REALM")
		if kcURL != "" && realm != "" {
			discoveryURL = kcURL + "/realms/" + realm
		}
	}

	// Build the external issuer URL from PANEL_HOSTNAME if available.
	// Keycloak's KC_HOSTNAME is set to the external panel URL, so the discovery
	// document returns the external URL as issuer even when fetched via internal URL.
	panelHostname := os.Getenv("PANEL_HOSTNAME")
	realm := os.Getenv("KEYCLOAK_REALM")
	if panelHostname != "" && realm != "" {
		host := strings.TrimRight(panelHostname, "/")
		if !strings.HasPrefix(host, "http") {
			host = "https://" + host
		}
		externalIssuer = host + "/realms/" + realm
	}

	// If OIDC is not configured, skip validation (development mode).
	if discoveryURL == "" || clientID == "" {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	log.Printf("OIDC: Initializing provider — discovery URL: %s, external issuer: %s, client ID: %s", discoveryURL, externalIssuer, clientID)

	// InsecureIssuerURLContext tells go-oidc to accept the issuer in the discovery document
	// even if it doesn't match the URL we used to fetch it. We pass the external issuer
	// (what Keycloak returns) so the library accepts the mismatch.
	ctx := context.Background()

	// Use a TLS-skipping HTTP client so the provider can reach the JWKS endpoint
	// via the external HTTPS URL (which uses a self-signed certificate).
	// The go-oidc library reads the HTTP client from the context via oauth2.HTTPClient.
	insecureClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, insecureClient)

	if externalIssuer != "" {
		ctx = oidc.InsecureIssuerURLContext(ctx, externalIssuer)
	}

	// Lazy-init OIDC provider: if startup init fails, retry on each request
	// until it succeeds. This handles DNS/network issues during pod startup
	// (e.g., Keycloak not yet reachable due to RAM pressure or slow DNS).
	type oidcState struct {
		provider *oidc.Provider
		verifier *oidc.IDTokenVerifier
		ctx      context.Context
	}

	var (
		oidcMu    sync.Mutex
		oidcReady *oidcState
	)

	initProvider := func() *oidcState {
		p, err := oidc.NewProvider(ctx, discoveryURL)
		if err != nil {
			log.Printf("WARNING: OIDC provider init failed: %v", err)
			return nil
		}
		log.Printf("OIDC: Provider initialized successfully")
		v := p.Verifier(&oidc.Config{
			ClientID:          clientID,
			SkipIssuerCheck:   true,
			SkipClientIDCheck: true,
		})
		return &oidcState{provider: p, verifier: v, ctx: ctx}
	}

	// Try initial init — if it fails, we'll retry lazily on each request
	if s := initProvider(); s != nil {
		oidcReady = s
	} else {
		log.Printf("OIDC: Will retry provider init on incoming requests")
	}

	getOIDC := func() *oidcState {
		oidcMu.Lock()
		defer oidcMu.Unlock()
		if oidcReady != nil {
			return oidcReady
		}
		if s := initProvider(); s != nil {
			oidcReady = s
			return s
		}
		return nil
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health check bypass: let /healthz through without OIDC
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				next.ServeHTTP(w, r)
				return
			}

			state := getOIDC()
			if state == nil {
				writeMiddlewareError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "OIDC provider not available — retrying init")
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing Authorization header")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid Authorization header format")
				return
			}

			// Use providerCtx (with insecure HTTP client) so JWKS keys can be fetched
			// from the external HTTPS URL with self-signed cert.
			idToken, err := state.verifier.Verify(state.ctx, parts[1])
			if err != nil {
				log.Printf("OIDC: token verification failed: %v", err)
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or expired token")
				return
			}

			var rc rawClaims
			if err := idToken.Claims(&rc); err != nil {
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Failed to parse token claims")
				return
			}

			claims := &TokenClaims{
				Subject:  rc.Subject,
				Email:    rc.Email,
				Username: rc.Username,
				Roles:    rc.RealmAccess.Roles,
			}

			// If the caller is an admin and sends X-Impersonate-User, override the
			// username so all resource-filtering logic sees the impersonated user.
			if impersonateAs := r.Header.Get("X-Impersonate-User"); impersonateAs != "" {
				isAdmin := false
				for _, role := range claims.Roles {
					if role == "admin" {
						isAdmin = true
						break
					}
				}
				if isAdmin {
					log.Printf("OIDC: admin %s impersonating user %s", claims.Username, impersonateAs)
					claims = &TokenClaims{
						Subject:  claims.Subject, // keep admin's subject for audit
						Email:    claims.Email,
						Username: impersonateAs,
						Roles:    []string{"user"}, // user role only — no admin privileges
					}
				} else {
					log.Printf("OIDC: X-Impersonate-User header ignored — caller %s is not admin (roles: %v)", claims.Username, claims.Roles)
				}
			}

			rCtx := context.WithValue(r.Context(), ClaimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(rCtx))
		})
	}
}

// GetClaims extracts TokenClaims from the request context.
func GetClaims(ctx context.Context) *TokenClaims {
	claims, _ := ctx.Value(ClaimsContextKey).(*TokenClaims)
	return claims
}
