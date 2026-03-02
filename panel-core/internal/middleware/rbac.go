package middleware

import (
	"fmt"
	"net/http"
)

// RequireRole returns middleware that checks if the authenticated user has one of the required roles.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
				return
			}

			for _, role := range claims.Roles {
				if allowed[role] {
					next.ServeHTTP(w, r)
					return
				}
			}

			writeMiddlewareError(w, http.StatusForbidden, "FORBIDDEN", "Insufficient permissions")
		})
	}
}

// RequireAdmin is a convenience wrapper for RequireRole("admin").
func RequireAdmin(next http.Handler) http.Handler {
	return RequireRole("admin")(next)
}

// HasRole checks if the claims contain a specific role.
func HasRole(claims *TokenClaims, role string) bool {
	if claims == nil {
		return false
	}
	for _, r := range claims.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// RequireOwnership returns middleware that enforces resource ownership for non-admin users.
func RequireOwnership(namespaceParam string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r.Context())
			if claims == nil {
				writeMiddlewareError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
				return
			}

			if HasRole(claims, "admin") {
				next.ServeHTTP(w, r)
				return
			}

			userNamespace := fmt.Sprintf("hosting-user-%s", claims.Username)

			targetNS := ""
			if namespaceParam != "" {
				targetNS = r.URL.Query().Get(namespaceParam)
			}
			if targetNS == "" {
				targetNS = r.Header.Get("X-Hosting-Namespace")
			}

			if targetNS == "" {
				next.ServeHTTP(w, r)
				return
			}

			if targetNS != userNamespace {
				writeMiddlewareError(w, http.StatusForbidden, "FORBIDDEN", "Access denied: resource belongs to another user")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// IsOwner checks if the authenticated user owns a resource in the given namespace.
func IsOwner(claims *TokenClaims, resourceNamespace string) bool {
	if claims == nil {
		return false
	}
	if HasRole(claims, "admin") {
		return true
	}
	userNamespace := fmt.Sprintf("hosting-user-%s", claims.Username)
	return resourceNamespace == userNamespace
}

// IsOwnerByLabel checks if the authenticated user owns a resource based on the
// hosting.panel/user label. Used when all resources live in a shared namespace.
func IsOwnerByLabel(claims *TokenClaims, obj interface{ GetLabels() map[string]string }) bool {
	if claims == nil {
		return false
	}
	if HasRole(claims, "admin") {
		return true
	}
	labels := obj.GetLabels()
	if labels == nil {
		return false
	}
	return labels["hosting.panel/user"] == claims.Username
}
