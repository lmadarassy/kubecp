package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// middlewareErrorResponse mirrors the JSON error structure written by writeMiddlewareError.
type middlewareErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestRequireRole_AdminAllowed(t *testing.T) {
	claims := &TokenClaims{Subject: "1", Username: "admin1", Roles: []string{"admin"}}
	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireRole_UserDeniedAdminRoute(t *testing.T) {
	claims := &TokenClaims{Subject: "2", Username: "user1", Roles: []string{"user"}}
	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp middlewareErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Error.Code != "FORBIDDEN" {
		t.Errorf("code = %q, want %q", resp.Error.Code, "FORBIDDEN")
	}
}

func TestRequireRole_NoClaims(t *testing.T) {
	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRequireRole_MultipleRolesAllowed(t *testing.T) {
	claims := &TokenClaims{Subject: "3", Username: "user2", Roles: []string{"user"}}
	handler := RequireRole("admin", "user")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHasRole(t *testing.T) {
	claims := &TokenClaims{Roles: []string{"admin", "user"}}
	if !HasRole(claims, "admin") {
		t.Error("expected HasRole to return true for admin")
	}
	if !HasRole(claims, "user") {
		t.Error("expected HasRole to return true for user")
	}
	if HasRole(claims, "superadmin") {
		t.Error("expected HasRole to return false for superadmin")
	}
	if HasRole(nil, "admin") {
		t.Error("expected HasRole to return false for nil claims")
	}
}

func TestIsOwner_AdminOwnsEverything(t *testing.T) {
	claims := &TokenClaims{Username: "admin1", Roles: []string{"admin"}}
	if !IsOwner(claims, "hosting-user-someone") {
		t.Error("admin should own any namespace")
	}
}

func TestIsOwner_UserOwnsOwnNamespace(t *testing.T) {
	claims := &TokenClaims{Username: "johndoe", Roles: []string{"user"}}
	if !IsOwner(claims, "hosting-user-johndoe") {
		t.Error("user should own their own namespace")
	}
	if IsOwner(claims, "hosting-user-janedoe") {
		t.Error("user should not own another user's namespace")
	}
}

func TestIsOwner_NilClaims(t *testing.T) {
	if IsOwner(nil, "hosting-user-anyone") {
		t.Error("nil claims should not own anything")
	}
}

func TestRequireOwnership_AdminBypass(t *testing.T) {
	claims := &TokenClaims{Username: "admin1", Roles: []string{"admin"}}
	handler := RequireOwnership("ns")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/?ns=hosting-user-someone", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("admin should bypass ownership, got status %d", w.Code)
	}
}

func TestRequireOwnership_UserOwnNamespace(t *testing.T) {
	claims := &TokenClaims{Username: "johndoe", Roles: []string{"user"}}
	handler := RequireOwnership("ns")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/?ns=hosting-user-johndoe", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("user should access own namespace, got status %d", w.Code)
	}
}

func TestRequireOwnership_UserDeniedOtherNamespace(t *testing.T) {
	claims := &TokenClaims{Username: "johndoe", Roles: []string{"user"}}
	handler := RequireOwnership("ns")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/?ns=hosting-user-janedoe", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsContextKey, claims))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("user should be denied other namespace, got status %d", w.Code)
	}
}
