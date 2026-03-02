package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// AdminClient provides access to the Keycloak Admin REST API.
// It handles service account authentication and token lifecycle.
type AdminClient struct {
	adminURL string
	issuerURL string
	username string
	password string
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// User represents a Keycloak user resource.
type User struct {
	ID            string            `json:"id,omitempty"`
	Username      string            `json:"username"`
	Email         string            `json:"email,omitempty"`
	FirstName     string            `json:"firstName,omitempty"`
	LastName      string            `json:"lastName,omitempty"`
	Enabled       bool              `json:"enabled"`
	EmailVerified bool              `json:"emailVerified,omitempty"`
	Attributes    map[string][]string `json:"attributes,omitempty"`
}

// Role represents a Keycloak realm role.
type Role struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// PasswordCredential represents a password to set for a user.
type PasswordCredential struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Temporary bool   `json:"temporary"`
}

// AdminError represents an error from the Keycloak Admin API.
type AdminError struct {
	StatusCode int
	Message    string
}

func (e *AdminError) Error() string {
	return fmt.Sprintf("keycloak admin api: %d %s", e.StatusCode, e.Message)
}

// NewAdminClient creates a new Keycloak Admin API client.
// Configuration is read from environment variables:
//   - KEYCLOAK_ADMIN_URL: Admin API base URL (e.g., https://keycloak.example.com/admin/realms/hosting)
//   - KEYCLOAK_ISSUER_URL: Token endpoint issuer (e.g., https://keycloak.example.com/realms/hosting)
//   - KEYCLOAK_ADMIN_USERNAME: Service account username
//   - KEYCLOAK_ADMIN_PASSWORD: Service account password
func NewAdminClient() *AdminClient {
	return &AdminClient{
		adminURL:   os.Getenv("KEYCLOAK_ADMIN_URL"),
		issuerURL:  os.Getenv("KEYCLOAK_ISSUER_URL"),
		username:   os.Getenv("KEYCLOAK_ADMIN_USERNAME"),
		password:   os.Getenv("KEYCLOAK_ADMIN_PASSWORD"),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewAdminClientWithConfig creates an AdminClient with explicit configuration (for testing).
func NewAdminClientWithConfig(adminURL, issuerURL, username, password string) *AdminClient {
	return &AdminClient{
		adminURL:   adminURL,
		issuerURL:  issuerURL,
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured returns true if the admin client has the required configuration.
func (c *AdminClient) Configured() bool {
	return c.adminURL != "" && c.issuerURL != "" && c.username != "" && c.password != ""
}

// getToken acquires or refreshes the service account access token using
// the Resource Owner Password Credentials grant against the master realm.
// The token URL is derived from adminURL to ensure the token issuer matches
// the admin API URL (required by Keycloak 26.x hostname strict validation).
func (c *AdminClient) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}

	// Derive token endpoint from adminURL.
	// adminURL is like http://keycloak.svc:80/admin/realms/hosting
	// We need http://keycloak.svc:80/realms/master/protocol/openid-connect/token
	baseURL := c.adminURL
	if idx := strings.Index(baseURL, "/admin/"); idx != -1 {
		baseURL = baseURL[:idx]
	} else if idx := strings.Index(baseURL, "/realms/"); idx != -1 {
		baseURL = baseURL[:idx]
	}
	tokenURL := baseURL + "/realms/master/protocol/openid-connect/token"

	data := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {c.username},
		"password":   {c.password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &AdminError{StatusCode: resp.StatusCode, Message: string(body)}
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	c.accessToken = tokenResp.AccessToken
	// Refresh 30 seconds before actual expiry.
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn-30) * time.Second)

	return c.accessToken, nil
}

// doRequest executes an authenticated request against the Keycloak Admin API.
func (c *AdminClient) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire admin token: %w", err)
	}

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	reqURL := c.adminURL + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// parseError reads an error response body and returns an AdminError.
func parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	msg := string(body)
	if msg == "" {
		msg = resp.Status
	}
	return &AdminError{StatusCode: resp.StatusCode, Message: msg}
}

// --- User CRUD ---

// CreateUser creates a new user in the Keycloak realm.
func (c *AdminClient) CreateUser(ctx context.Context, user User) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/users", user)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return "", &AdminError{StatusCode: 409, Message: "user already exists"}
	}
	if resp.StatusCode != http.StatusCreated {
		return "", parseError(resp)
	}

	// Extract user ID from Location header.
	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("no Location header in create user response")
	}
	parts := strings.Split(location, "/")
	return parts[len(parts)-1], nil
}

// GetUser retrieves a user by ID.
func (c *AdminClient) GetUser(ctx context.Context, userID string) (*User, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/users/"+userID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &AdminError{StatusCode: 404, Message: "user not found"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}
	return &user, nil
}

// UpdateUser updates an existing user.
func (c *AdminClient) UpdateUser(ctx context.Context, userID string, user User) error {
	resp, err := c.doRequest(ctx, http.MethodPut, "/users/"+userID, user)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}

// DeleteUser deletes a user by ID.
func (c *AdminClient) DeleteUser(ctx context.Context, userID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/users/"+userID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}

// ListUsers lists users, optionally filtered by query parameters.
func (c *AdminClient) ListUsers(ctx context.Context, search string, first, max int) ([]User, error) {
	path := fmt.Sprintf("/users?first=%d&max=%d", first, max)
	if search != "" {
		path += "&search=" + url.QueryEscape(search)
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}

// --- Role Management ---

// GetRealmRole retrieves a realm role by name.
func (c *AdminClient) GetRealmRole(ctx context.Context, roleName string) (*Role, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/roles/"+roleName, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}

	var role Role
	if err := json.NewDecoder(resp.Body).Decode(&role); err != nil {
		return nil, fmt.Errorf("decode role: %w", err)
	}
	return &role, nil
}

// AssignRole assigns a realm role to a user.
func (c *AdminClient) AssignRole(ctx context.Context, userID, roleName string) error {
	role, err := c.GetRealmRole(ctx, roleName)
	if err != nil {
		return fmt.Errorf("get role %q: %w", roleName, err)
	}

	resp, err := c.doRequest(ctx, http.MethodPost,
		"/users/"+userID+"/role-mappings/realm",
		[]Role{*role},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}

// RemoveRole removes a realm role from a user.
func (c *AdminClient) RemoveRole(ctx context.Context, userID, roleName string) error {
	role, err := c.GetRealmRole(ctx, roleName)
	if err != nil {
		return fmt.Errorf("get role %q: %w", roleName, err)
	}

	resp, err := c.doRequest(ctx, http.MethodDelete,
		"/users/"+userID+"/role-mappings/realm",
		[]Role{*role},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}

// --- Password Management ---

// SetPassword sets a user's password.
func (c *AdminClient) SetPassword(ctx context.Context, userID, password string, temporary bool) error {
	cred := PasswordCredential{
		Type:      "password",
		Value:     password,
		Temporary: temporary,
	}

	resp, err := c.doRequest(ctx, http.MethodPut,
		"/users/"+userID+"/reset-password",
		cred,
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return parseError(resp)
	}
	return nil
}
