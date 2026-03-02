package keycloak

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setupMockKeycloak creates a mock Keycloak server for testing.
func setupMockKeycloak(t *testing.T) (*httptest.Server, *AdminClient) {
	t.Helper()

	users := make(map[string]User)
	nextID := 1

	mux := http.NewServeMux()

	// Token endpoint (master realm)
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "mock-admin-token",
			"expires_in":   300,
			"token_type":   "Bearer",
		})
	})

	// Create user
	mux.HandleFunc("/admin/realms/hosting/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var u User
			if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// Check for duplicate username
			for _, existing := range users {
				if existing.Username == u.Username {
					w.WriteHeader(http.StatusConflict)
					return
				}
			}
			id := idStr(nextID)
			nextID++
			u.ID = id
			users[id] = u
			w.Header().Set("Location", "/admin/realms/hosting/users/"+id)
			w.WriteHeader(http.StatusCreated)

		case http.MethodGet:
			var result []User
			search := r.URL.Query().Get("search")
			for _, u := range users {
				if search == "" || strings.Contains(u.Username, search) || strings.Contains(u.Email, search) {
					result = append(result, u)
				}
			}
			if result == nil {
				result = []User{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		}
	})

	// Get/Update/Delete user by ID
	mux.HandleFunc("/admin/realms/hosting/users/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Handle role-mappings
		if strings.Contains(path, "/role-mappings/realm") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Handle reset-password
		if strings.Contains(path, "/reset-password") {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		parts := strings.Split(strings.TrimPrefix(path, "/admin/realms/hosting/users/"), "/")
		id := parts[0]

		switch r.Method {
		case http.MethodGet:
			u, ok := users[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(u)

		case http.MethodPut:
			if _, ok := users[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var u User
			if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			u.ID = id
			users[id] = u
			w.WriteHeader(http.StatusNoContent)

		case http.MethodDelete:
			if _, ok := users[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(users, id)
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// Roles endpoint
	mux.HandleFunc("/admin/realms/hosting/roles/", func(w http.ResponseWriter, r *http.Request) {
		roleName := strings.TrimPrefix(r.URL.Path, "/admin/realms/hosting/roles/")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Role{ID: "role-" + roleName, Name: roleName})
	})

	server := httptest.NewServer(mux)

	client := NewAdminClientWithConfig(
		server.URL+"/admin/realms/hosting",
		server.URL+"/realms/hosting",
		"admin",
		"admin-pass",
	)

	return server, client
}

func idStr(n int) string {
	return "user-" + strings.Repeat("0", 3-len(string(rune('0'+n)))) + string(rune('0'+n))
}

func TestCreateAndGetUser(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	// Create user
	id, err := client.CreateUser(ctx, User{
		Username:  "testuser",
		Email:     "test@example.com",
		FirstName: "Test",
		LastName:  "User",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if id == "" {
		t.Fatal("CreateUser returned empty ID")
	}

	// Get user
	user, err := client.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if user.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", user.Username)
	}
	if user.Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got %q", user.Email)
	}
}

func TestCreateDuplicateUser(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	_, err := client.CreateUser(ctx, User{Username: "dup", Enabled: true})
	if err != nil {
		t.Fatalf("first CreateUser failed: %v", err)
	}

	_, err = client.CreateUser(ctx, User{Username: "dup", Enabled: true})
	if err == nil {
		t.Fatal("expected error for duplicate user, got nil")
	}
	adminErr, ok := err.(*AdminError)
	if !ok {
		t.Fatalf("expected AdminError, got %T", err)
	}
	if adminErr.StatusCode != 409 {
		t.Errorf("expected status 409, got %d", adminErr.StatusCode)
	}
}

func TestUpdateUser(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	id, err := client.CreateUser(ctx, User{
		Username: "updateme",
		Email:    "old@example.com",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	err = client.UpdateUser(ctx, id, User{
		Username: "updateme",
		Email:    "new@example.com",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}

	user, err := client.GetUser(ctx, id)
	if err != nil {
		t.Fatalf("GetUser failed: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Errorf("expected email 'new@example.com', got %q", user.Email)
	}
}

func TestDeleteUser(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	id, err := client.CreateUser(ctx, User{Username: "deleteme", Enabled: true})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	err = client.DeleteUser(ctx, id)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	_, err = client.GetUser(ctx, id)
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
	adminErr, ok := err.(*AdminError)
	if !ok {
		t.Fatalf("expected AdminError, got %T", err)
	}
	if adminErr.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", adminErr.StatusCode)
	}
}

func TestListUsers(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	_, _ = client.CreateUser(ctx, User{Username: "alice", Enabled: true})
	_, _ = client.CreateUser(ctx, User{Username: "bob", Enabled: true})

	users, err := client.ListUsers(ctx, "", 0, 100)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestAssignRole(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	id, _ := client.CreateUser(ctx, User{Username: "roleuser", Enabled: true})

	err := client.AssignRole(ctx, id, "user")
	if err != nil {
		t.Fatalf("AssignRole failed: %v", err)
	}
}

func TestSetPassword(t *testing.T) {
	server, client := setupMockKeycloak(t)
	defer server.Close()

	ctx := context.Background()

	id, _ := client.CreateUser(ctx, User{Username: "pwuser", Enabled: true})

	err := client.SetPassword(ctx, id, "newpassword123", false)
	if err != nil {
		t.Fatalf("SetPassword failed: %v", err)
	}
}

func TestConfigured(t *testing.T) {
	c := NewAdminClientWithConfig("", "", "", "")
	if c.Configured() {
		t.Error("expected Configured() to return false for empty config")
	}

	c = NewAdminClientWithConfig("http://kc/admin", "http://kc/realms/hosting", "admin", "pass")
	if !c.Configured() {
		t.Error("expected Configured() to return true for valid config")
	}
}
