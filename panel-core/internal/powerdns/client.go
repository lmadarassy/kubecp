package powerdns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Zone represents a PowerDNS zone.
type Zone struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`           // Native, Master, Slave
	DNSSec         bool     `json:"dnssec"`
	Serial         int64    `json:"serial"`
	NotifiedSerial int64    `json:"notified_serial"`
	Masters        []string `json:"masters,omitempty"`
	Account        string   `json:"account,omitempty"`
	RRSets         []RRSet  `json:"rrsets,omitempty"`
}

// RRSet represents a set of DNS records with the same name and type.
type RRSet struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	TTL        int      `json:"ttl"`
	ChangeType string   `json:"changetype,omitempty"` // REPLACE or DELETE
	Records    []Record `json:"records"`
	Comments   []Comment `json:"comments,omitempty"`
}

// Record represents a single DNS record value.
type Record struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

// Comment represents a comment on an RRSet.
type Comment struct {
	Content    string `json:"content"`
	Account    string `json:"account"`
	ModifiedAt int64  `json:"modified_at"`
}

// ZonePatch is used to update records in a zone via PATCH.
type ZonePatch struct {
	RRSets []RRSet `json:"rrsets"`
}

// Client is a PowerDNS REST API client.
type Client struct {
	baseURL    string
	apiKey     string
	serverID   string
	httpClient *http.Client
}

// NewClient creates a new PowerDNS API client.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		serverID: "localhost",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Configured returns true if the client has a base URL and API key.
func (c *Client) Configured() bool {
	return c.baseURL != "" && c.apiKey != ""
}

func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := fmt.Sprintf("%s/api/v1/servers/%s%s", c.baseURL, c.serverID, path)
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

func decodeOrClose(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func drainAndClose(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// APIError represents an error returned by the PowerDNS API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("powerdns: %d %s", e.StatusCode, e.Message)
}

func checkResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	msg := string(body)
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}

// ListZones returns all zones from PowerDNS.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/zones", nil)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var zones []Zone
	if err := decodeOrClose(resp, &zones); err != nil {
		return nil, fmt.Errorf("decode zones: %w", err)
	}
	return zones, nil
}

// GetZone returns a single zone with all RRSets.
func (c *Client) GetZone(ctx context.Context, zoneID string) (*Zone, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/zones/"+zoneID, nil)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var zone Zone
	if err := decodeOrClose(resp, &zone); err != nil {
		return nil, fmt.Errorf("decode zone: %w", err)
	}
	return &zone, nil
}

// CreateZone creates a new zone in PowerDNS.
func (c *Client) CreateZone(ctx context.Context, zone Zone) (*Zone, error) {
	resp, err := c.doRequest(ctx, http.MethodPost, "/zones", zone)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(resp); err != nil {
		return nil, err
	}
	var created Zone
	if err := decodeOrClose(resp, &created); err != nil {
		return nil, fmt.Errorf("decode created zone: %w", err)
	}
	return &created, nil
}

// DeleteZone deletes a zone from PowerDNS.
func (c *Client) DeleteZone(ctx context.Context, zoneID string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, "/zones/"+zoneID, nil)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
}

// PatchZone updates records in a zone using PATCH (add/replace/delete RRSets).
func (c *Client) PatchZone(ctx context.Context, zoneID string, patch ZonePatch) error {
	resp, err := c.doRequest(ctx, http.MethodPatch, "/zones/"+zoneID, patch)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
}

// NotifyZone sends a NOTIFY to all secondary servers for the zone.
func (c *Client) NotifyZone(ctx context.Context, zoneID string) error {
	resp, err := c.doRequest(ctx, http.MethodPut, "/zones/"+zoneID+"/notify", nil)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
}

// UpdateZoneMetadata updates zone metadata (e.g., allow-axfr-ips).
func (c *Client) UpdateZoneMetadata(ctx context.Context, zoneID, kind string, metadata []string) error {
	body := map[string]interface{}{
		"kind":     kind,
		"metadata": metadata,
	}
	resp, err := c.doRequest(ctx, http.MethodPut, "/zones/"+zoneID+"/metadata/"+kind, body)
	if err != nil {
		return err
	}
	drainAndClose(resp)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return &APIError{StatusCode: resp.StatusCode, Message: resp.Status}
}