package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// AuthHandler manages OIDC Authorization Code Flow + PKCE with Keycloak.
type AuthHandler struct {
	oauth2Config *oauth2.Config
	oidcProvider *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	panelBaseURL string
	issuerURL    string

	// In-memory session store: sessionID → Session
	mu       sync.RWMutex
	sessions map[string]*Session

	// In-memory PKCE state store: state → pkceParams
	stateMu sync.Mutex
	states  map[string]*pkceState
}

// Session holds token data for an authenticated user session.
type Session struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	Expiry       time.Time `json:"expiry"`
}

// pkceState stores PKCE parameters for an in-flight authorization request.
type pkceState struct {
	codeVerifier string
	createdAt    time.Time
}

// NewAuthHandler creates an AuthHandler configured from environment variables.
// Returns nil if OIDC is not configured (development mode).
func NewAuthHandler() *AuthHandler {
	issuerURL := os.Getenv("KEYCLOAK_ISSUER_URL")
	clientID := os.Getenv("KEYCLOAK_CLIENT_ID")
	clientSecret := os.Getenv("KEYCLOAK_CLIENT_SECRET")
	panelBaseURL := os.Getenv("PANEL_BASE_URL")

	if issuerURL == "" || clientID == "" || panelBaseURL == "" {
		return nil
	}

	provider, err := oidc.NewProvider(context.Background(), issuerURL)
	if err != nil {
		log.Printf("WARNING: OIDC provider init failed: %v", err)
		return nil
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  panelBaseURL + "/api/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})

	return &AuthHandler{
		oauth2Config: oauth2Cfg,
		oidcProvider: provider,
		verifier:     verifier,
		panelBaseURL: panelBaseURL,
		issuerURL:    issuerURL,
		sessions:     make(map[string]*Session),
		states:       make(map[string]*pkceState),
	}
}

// generateRandomString returns a cryptographically random URL-safe string.
func generateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeVerifier creates a PKCE code verifier (43-128 chars).
func generateCodeVerifier() (string, error) {
	return generateRandomString(32)
}

// codeChallenge computes the S256 code challenge from a verifier.
func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateSessionID creates a random session identifier.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LoginHandler initiates the OIDC Authorization Code Flow + PKCE.
// GET /api/auth/login → redirects to Keycloak login page.
func (h *AuthHandler) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := generateRandomString(16)
		if err != nil {
			WriteInternalError(w, "Failed to generate state")
			return
		}

		verifier, err := generateCodeVerifier()
		if err != nil {
			WriteInternalError(w, "Failed to generate PKCE verifier")
			return
		}

		// Store PKCE state
		h.stateMu.Lock()
		h.states[state] = &pkceState{
			codeVerifier: verifier,
			createdAt:    time.Now(),
		}
		h.stateMu.Unlock()

		challenge := codeChallenge(verifier)

		authURL := h.oauth2Config.AuthCodeURL(state,
			oauth2.SetAuthURLParam("code_challenge", challenge),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)

		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

// CallbackHandler handles the Keycloak OIDC callback.
// GET /api/auth/callback?code=...&state=...
func (h *AuthHandler) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")

		if code == "" || state == "" {
			errMsg := r.URL.Query().Get("error_description")
			if errMsg == "" {
				errMsg = "Missing code or state parameter"
			}
			WriteBadRequest(w, errMsg, nil)
			return
		}

		// Retrieve and validate PKCE state
		h.stateMu.Lock()
		ps, ok := h.states[state]
		if ok {
			delete(h.states, state)
		}
		h.stateMu.Unlock()

		if !ok {
			WriteBadRequest(w, "Invalid or expired state parameter", nil)
			return
		}

		// Reject states older than 10 minutes
		if time.Since(ps.createdAt) > 10*time.Minute {
			WriteBadRequest(w, "State parameter expired", nil)
			return
		}

		// Exchange authorization code for tokens with PKCE verifier
		token, err := h.oauth2Config.Exchange(r.Context(), code,
			oauth2.SetAuthURLParam("code_verifier", ps.codeVerifier),
		)
		if err != nil {
			log.Printf("Token exchange failed: %v", err)
			WriteUnauthorized(w, "Token exchange failed")
			return
		}

		// Extract and verify ID token
		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok {
			WriteInternalError(w, "No id_token in token response")
			return
		}

		_, err = h.verifier.Verify(r.Context(), rawIDToken)
		if err != nil {
			WriteUnauthorized(w, "ID token verification failed")
			return
		}

		// Create session
		sessionID, err := generateSessionID()
		if err != nil {
			WriteInternalError(w, "Failed to create session")
			return
		}

		h.mu.Lock()
		h.sessions[sessionID] = &Session{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			IDToken:      rawIDToken,
			Expiry:       token.Expiry,
		}
		h.mu.Unlock()

		// Set secure HTTP-only session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "panel_session",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400, // 24 hours
		})

		// Redirect to dashboard
		http.Redirect(w, r, h.panelBaseURL+"/dashboard", http.StatusFound)
	}
}

// LogoutHandler clears the session and optionally redirects to Keycloak logout.
// POST /api/auth/logout
func (h *AuthHandler) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("panel_session")
		if err == nil && cookie.Value != "" {
			h.mu.Lock()
			session, exists := h.sessions[cookie.Value]
			delete(h.sessions, cookie.Value)
			h.mu.Unlock()

			// Clear the cookie
			http.SetCookie(w, &http.Cookie{
				Name:     "panel_session",
				Value:    "",
				Path:     "/",
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			})

			// Redirect to Keycloak end_session_endpoint if we have an ID token
			if exists && session.IDToken != "" {
				logoutURL := h.issuerURL + "/protocol/openid-connect/logout" +
					"?id_token_hint=" + session.IDToken +
					"&post_logout_redirect_uri=" + h.panelBaseURL
				http.Redirect(w, r, logoutURL, http.StatusFound)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
	}
}

// RefreshHandler refreshes the access token using the stored refresh token.
// POST /api/auth/refresh
func (h *AuthHandler) RefreshHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("panel_session")
		if err != nil || cookie.Value == "" {
			WriteUnauthorized(w, "No active session")
			return
		}

		h.mu.RLock()
		session, exists := h.sessions[cookie.Value]
		h.mu.RUnlock()

		if !exists {
			WriteUnauthorized(w, "Session not found")
			return
		}

		if session.RefreshToken == "" {
			WriteUnauthorized(w, "No refresh token available")
			return
		}

		// Use the refresh token to get new tokens
		tokenSource := h.oauth2Config.TokenSource(r.Context(), &oauth2.Token{
			RefreshToken: session.RefreshToken,
		})

		newToken, err := tokenSource.Token()
		if err != nil {
			log.Printf("Token refresh failed: %v", err)
			// Clear invalid session
			h.mu.Lock()
			delete(h.sessions, cookie.Value)
			h.mu.Unlock()
			WriteUnauthorized(w, "Token refresh failed, please login again")
			return
		}

		// Extract new ID token if present
		rawIDToken, _ := newToken.Extra("id_token").(string)
		if rawIDToken == "" {
			rawIDToken = session.IDToken
		}

		// Update session
		h.mu.Lock()
		h.sessions[cookie.Value] = &Session{
			AccessToken:  newToken.AccessToken,
			RefreshToken: newToken.RefreshToken,
			IDToken:      rawIDToken,
			Expiry:       newToken.Expiry,
		}
		h.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":     "refreshed",
			"expires_at": newToken.Expiry,
		})
	}
}

// GetSession retrieves the session for a given session cookie value.
func (h *AuthHandler) GetSession(sessionID string) *Session {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[sessionID]
}

// CleanExpiredStates removes PKCE states older than 10 minutes.
// Should be called periodically.
func (h *AuthHandler) CleanExpiredStates() {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, v := range h.states {
		if v.createdAt.Before(cutoff) {
			delete(h.states, k)
		}
	}
}
