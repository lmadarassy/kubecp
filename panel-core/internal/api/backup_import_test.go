package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// createTestArchive builds a tar.gz archive with the given directory/file entries.
// Each entry is a path string. Paths ending with "/" are directories, others are files.
func createTestArchive(t *testing.T, entries []string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, entry := range entries {
		if entry[len(entry)-1] == '/' {
			// Directory
			if err := tw.WriteHeader(&tar.Header{
				Name:     entry,
				Typeflag: tar.TypeDir,
				Mode:     0755,
			}); err != nil {
				t.Fatalf("failed to write dir header %q: %v", entry, err)
			}
		} else {
			// File with dummy content
			content := []byte("test-content")
			if err := tw.WriteHeader(&tar.Header{
				Name:     entry,
				Typeflag: tar.TypeReg,
				Mode:     0644,
				Size:     int64(len(content)),
			}); err != nil {
				t.Fatalf("failed to write file header %q: %v", entry, err)
			}
			if _, err := tw.Write(content); err != nil {
				t.Fatalf("failed to write file content %q: %v", entry, err)
			}
		}
	}

	tw.Close()
	gz.Close()
	return &buf
}

func setupImportRouter(handler *ImportHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/backups/import", handler.HandleImport)
	return r
}

func makeMultipartRequest(t *testing.T, archive io.Reader, username string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add the backup file
	part, err := writer.CreateFormFile("backup", "backup.tar.gz")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := io.Copy(part, archive); err != nil {
		t.Fatalf("failed to copy archive: %v", err)
	}

	// Add the username field
	if err := writer.WriteField("username", username); err != nil {
		t.Fatalf("failed to write username field: %v", err)
	}
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/backups/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// --- ParseVestaArchive Tests ---

func TestParseVestaArchive_Valid(t *testing.T) {
	archive := createTestArchive(t, []string{
		"web/",
		"web/example.com/",
		"web/test.org/",
		"db/mydb.sql",
		"mail/",
		"mail/example.com/",
		"dns/example.com.conf",
		"user/",
		"user/user.conf",
	})

	result, err := ParseVestaArchive(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasWeb {
		t.Error("HasWeb should be true")
	}
	if !result.HasDB {
		t.Error("HasDB should be true")
	}
	if !result.HasMail {
		t.Error("HasMail should be true")
	}
	if !result.HasDNS {
		t.Error("HasDNS should be true")
	}
	if !result.HasUser {
		t.Error("HasUser should be true")
	}
	if len(result.WebDirs) != 2 {
		t.Errorf("WebDirs = %v, want 2 entries", result.WebDirs)
	}
	if len(result.DBFiles) != 1 {
		t.Errorf("DBFiles = %v, want 1 entry", result.DBFiles)
	}
	if len(result.MailDirs) != 1 {
		t.Errorf("MailDirs = %v, want 1 entry", result.MailDirs)
	}
	if len(result.DNSFiles) != 1 {
		t.Errorf("DNSFiles = %v, want 1 entry", result.DNSFiles)
	}
}

func TestParseVestaArchive_WebOnly(t *testing.T) {
	archive := createTestArchive(t, []string{
		"web/",
		"web/mysite.com/",
	})

	result, err := ParseVestaArchive(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasWeb {
		t.Error("HasWeb should be true")
	}
	if result.HasDB {
		t.Error("HasDB should be false")
	}
	if len(result.WebDirs) != 1 {
		t.Errorf("WebDirs = %v, want 1 entry", result.WebDirs)
	}
}

func TestParseVestaArchive_Invalid(t *testing.T) {
	// Archive with no recognized directories
	archive := createTestArchive(t, []string{
		"random/",
		"random/file.txt",
	})

	_, err := ParseVestaArchive(archive)
	if err == nil {
		t.Error("expected error for invalid archive, got nil")
	}
}

func TestParseVestaArchive_NotGzip(t *testing.T) {
	_, err := ParseVestaArchive(bytes.NewReader([]byte("not a gzip file")))
	if err == nil {
		t.Error("expected error for non-gzip data, got nil")
	}
}

func TestParseVestaArchive_EmptyArchive(t *testing.T) {
	// Valid gzip+tar but no entries
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.Close()
	gz.Close()

	_, err := ParseVestaArchive(&buf)
	if err == nil {
		t.Error("expected error for empty archive, got nil")
	}
}

// --- sanitizeName Tests ---

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"example.com", "example-com"},
		{"my_database", "my-database"},
		{"user@domain.com", "user-at-domain-com"},
		{"UPPERCASE", "uppercase"},
		{"---leading-trailing---", "leading-trailing"},
		{"special!@#chars", "special-at-chars"},
		{"", "imported"},
		{"a", "a"},
		{"valid-name-123", "valid-name-123"},
	}

	for _, tt := range tests {
		got := sanitizeName(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- HandleImport Tests ---

func TestHandleImport_Success(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewImportHandler(dynClient)
	router := setupImportRouter(handler)

	archive := createTestArchive(t, []string{
		"web/",
		"web/example.com/",
		"db/mydb.sql",
		"mail/",
		"mail/example.com/",
	})

	req := makeMultipartRequest(t, archive, "importuser")
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ImportResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "Completed" {
		t.Errorf("status = %q, want %q", resp.Status, "Completed")
	}
	if resp.Username != "importuser" {
		t.Errorf("username = %q, want %q", resp.Username, "importuser")
	}
	if resp.Namespace != "hosting-user-importuser" {
		t.Errorf("namespace = %q, want %q", resp.Namespace, "hosting-user-importuser")
	}
	if resp.Imported.Websites != 1 {
		t.Errorf("websites = %d, want 1", resp.Imported.Websites)
	}
	if resp.Imported.Databases != 1 {
		t.Errorf("databases = %d, want 1", resp.Imported.Databases)
	}
	if resp.Imported.Emails != 1 {
		t.Errorf("emails = %d, want 1", resp.Imported.Emails)
	}
}

func TestHandleImport_AdminOnly(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewImportHandler(dynClient)
	router := setupImportRouter(handler)

	archive := createTestArchive(t, []string{"web/", "web/site.com/"})
	req := makeMultipartRequest(t, archive, "someuser")
	req = withClaims(req, userClaims("testuser"))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestHandleImport_MissingFile(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewImportHandler(dynClient)
	router := setupImportRouter(handler)

	// POST without multipart form
	req := httptest.NewRequest(http.MethodPost, "/api/backups/import", nil)
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleImport_MissingUsername(t *testing.T) {
	dynClient := newBackupFakeDynClient()
	handler := NewImportHandler(dynClient)
	router := setupImportRouter(handler)

	// Create multipart with file but no username
	archive := createTestArchive(t, []string{"web/", "web/site.com/"})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("backup", "backup.tar.gz")
	io.Copy(part, archive)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/backups/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = withClaims(req, adminClaims())
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
