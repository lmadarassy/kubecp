package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hosting-panel/panel-core/internal/keycloak"
	"github.com/hosting-panel/panel-core/internal/middleware"
)

func setupMockKeycloakForAPI(t *testing.T) (*httptest.Server, *keycloak.AdminClient) {
	t.Helper()
	users := make(map[string]keycloak.User)
	nextID := 1
	mockID := func() string {
		id := fmt.Sprintf("kc-user-%d", nextID)
		nextID++
		return id
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"access_token": "mock-token", "expires_in": 300, "token_type": "Bearer"})
	})
	mux.HandleFunc("/admin/realms/hosting/users", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var u keycloak.User
			if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, existing := range users {
				if existing.Username == u.Username {
					w.WriteHeader(http.StatusConflict)
					return
				}
			}
			id := mockID()
			u.ID = id
			users[id] = u
			w.Header().Set("Location", "/admin/realms/hosting/users/"+id)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			search := r.URL.Query().Get("search")
			var result []keycloak.User
			for _, u := range users {
				if search == "" || strings.Contains(u.Username, search) || strings.Contains(u.Email, search) {
					result = append(result, u)
				}
			}
			if result == nil {
				result = []keycloak.User{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		}
	})
	mux.HandleFunc("/admin/realms/hosting/users/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, "/role-mappings/realm") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
			var u keycloak.User
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
	mux.HandleFunc("/admin/realms/hosting/roles/", func(w http.ResponseWriter, r *http.Request) {
		roleName := strings.TrimPrefix(r.URL.Path, "/admin/realms/hosting/roles/")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keycloak.Role{ID: "role-" + roleName, Name: roleName})
	})
	server := httptest.NewServer(mux)
	client := keycloak.NewAdminClientWithConfig(server.URL+"/admin/realms/hosting", server.URL+"/realms/hosting", "admin", "admin-pass")
	return server, client
}

func setupUserRouter(kcClient *keycloak.AdminClient) *chi.Mux {
	k8sClient := fake.NewSimpleClientset()
	handler := NewUserHandler(kcClient, k8sClient)
	r := chi.NewRouter()
	r.Route("/api/users", func(r chi.Router) { handler.RegisterRoutes(r) })
	return r
}

func TestCreateUser_Success(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "newuser", Email: "new@example.com", FirstName: "New", LastName: "User", Password: "secret123"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var resp UserResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Username != "newuser" {
		t.Errorf("username = %q, want %q", resp.Username, "newuser")
	}
	if resp.Email != "new@example.com" {
		t.Errorf("email = %q, want %q", resp.Email, "new@example.com")
	}
	if resp.Namespace != "hosting-user-newuser" {
		t.Errorf("namespace = %q, want %q", resp.Namespace, "hosting-user-newuser")
	}
	if resp.ID == "" {
		t.Error("expected non-empty user ID")
	}
}

func TestCreateUser_DuplicateUsername(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "dupuser", Email: "dup@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: status = %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestCreateUser_EmptyUsername(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "", Email: "no-name@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateUser_InvalidEmail(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "bademail", Email: "not-an-email"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetUser_SelfAccess(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "selfuser", Email: "self@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	req = httptest.NewRequest(http.MethodGet, "/api/users/"+created.ID, nil)
	req = withClaims(req, &middleware.TokenClaims{Subject: created.ID, Username: "selfuser", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp UserResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Username != "selfuser" {
		t.Errorf("username = %q, want %q", resp.Username, "selfuser")
	}
}

func TestGetUser_ForbiddenOtherUser(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "targetuser", Email: "target@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	req = httptest.NewRequest(http.MethodGet, "/api/users/"+created.ID, nil)
	req = withClaims(req, &middleware.TokenClaims{Subject: "different-user-id", Username: "otheruser", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestGetUser_AdminCanAccessAny(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "anyuser", Email: "any@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	req = httptest.NewRequest(http.MethodGet, "/api/users/"+created.ID, nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestUpdateUser_Success(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	createBody := CreateUserRequest{Username: "updateme", Email: "old@example.com", FirstName: "Old"}
	bodyBytes, _ := json.Marshal(createBody)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	updateBody := UpdateUserRequest{Email: "new@example.com", FirstName: "New"}
	bodyBytes, _ = json.Marshal(updateBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID, bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp UserResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Email != "new@example.com" {
		t.Errorf("email = %q, want %q", resp.Email, "new@example.com")
	}
	if resp.FirstName != "New" {
		t.Errorf("firstName = %q, want %q", resp.FirstName, "New")
	}
}

func TestUpdateUser_SelfAccess(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "selfupdate", Email: "su@example.com", FirstName: "Old"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	updateBody := UpdateUserRequest{FirstName: "Updated"}
	bodyBytes, _ = json.Marshal(updateBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID, bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: created.ID, Username: "selfupdate", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp UserResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.FirstName != "Updated" {
		t.Errorf("firstName = %q, want %q", resp.FirstName, "Updated")
	}
}

func TestUpdateUser_ForbiddenOtherUser(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "target", Email: "t@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	updateBody := UpdateUserRequest{FirstName: "Hacked"}
	bodyBytes, _ = json.Marshal(updateBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID, bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: "attacker-id", Username: "attacker", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestDeleteUser_Success(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "deleteme", Email: "del@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	req = httptest.NewRequest(http.MethodDelete, "/api/users/"+created.ID, nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/users/"+created.ID, nil)
	req = withClaims(req, adminClaims())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("after delete: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteUser_NotFound(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	req := httptest.NewRequest(http.MethodDelete, "/api/users/nonexistent-id", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestChangePassword_Success(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "pwuser", Email: "pw@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	pwBody := ChangePasswordRequest{Password: "newpass123"}
	bodyBytes, _ = json.Marshal(pwBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID+"/password", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: created.ID, Username: "pwuser", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestChangePassword_EmptyPassword(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "emptypw", Email: "ep@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	pwBody := ChangePasswordRequest{Password: ""}
	bodyBytes, _ = json.Marshal(pwBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID+"/password", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: created.ID, Username: "emptypw", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestChangePassword_ForbiddenOtherUser(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	body := CreateUserRequest{Username: "victim", Email: "v@example.com"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var created UserResponse
	json.NewDecoder(w.Body).Decode(&created)
	pwBody := ChangePasswordRequest{Password: "hacked"}
	bodyBytes, _ = json.Marshal(pwBody)
	req = httptest.NewRequest(http.MethodPut, "/api/users/"+created.ID+"/password", bytes.NewReader(bodyBytes))
	req = withClaims(req, &middleware.TokenClaims{Subject: "attacker-id", Username: "attacker", Roles: []string{"user"}})
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestListUsers_AdminOnly(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	for _, name := range []string{"alice", "bob"} {
		body := CreateUserRequest{Username: name, Email: name + "@example.com"}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
		req = withClaims(req, adminClaims())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var users []UserResponse
	json.NewDecoder(w.Body).Decode(&users)
	if len(users) != 2 {
		t.Errorf("got %d users, want 2", len(users))
	}
}

func TestListUsers_WithSearch(t *testing.T) {
	server, kcClient := setupMockKeycloakForAPI(t)
	defer server.Close()
	router := setupUserRouter(kcClient)
	for _, name := range []string{"alice", "bob", "charlie"} {
		body := CreateUserRequest{Username: name, Email: name + "@example.com"}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewReader(bodyBytes))
		req = withClaims(req, adminClaims())
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/users?search=ali", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var users []UserResponse
	json.NewDecoder(w.Body).Decode(&users)
	if len(users) != 1 {
		t.Errorf("got %d users, want 1", len(users))
	}
	if len(users) > 0 && users[0].Username != "alice" {
		t.Errorf("username = %q, want %q", users[0].Username, "alice")
	}
}
