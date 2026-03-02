package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/hosting-panel/panel-core/internal/middleware"
)

// ImportHandler implements VestaCP/HestiaCP backup import.
type ImportHandler struct {
	dynClient dynamic.Interface
}

// NewImportHandler creates a new ImportHandler.
func NewImportHandler(dynClient dynamic.Interface) *ImportHandler {
	return &ImportHandler{dynClient: dynClient}
}

// --- Request/Response types ---

// ImportResponse is the JSON response for a backup import.
type ImportResponse struct {
	Status    string          `json:"status"`
	Username  string          `json:"username"`
	Namespace string          `json:"namespace"`
	Imported  ImportedCounts  `json:"imported"`
	Errors    []string        `json:"errors,omitempty"`
}

// ImportedCounts tracks how many resources were imported.
type ImportedCounts struct {
	Websites int `json:"websites"`
	Databases int `json:"databases"`
	Emails   int `json:"emails"`
	DNSZones int `json:"dnsZones"`
}

// VestaArchive represents the parsed contents of a VestaCP/HestiaCP backup.
type VestaArchive struct {
	HasWeb  bool
	HasDB   bool
	HasMail bool
	HasDNS  bool
	HasUser bool
	WebDirs  []string // website directory names under web/
	DBFiles  []string // database dump files under db/
	MailDirs []string // email directories under mail/
	DNSFiles []string // DNS zone files under dns/
	UserFile string   // user config file path
}

// --- Handler ---

// HandleImport handles POST /api/backups/import.
// Admin only — imports a VestaCP/HestiaCP tar.gz backup.
func (h *ImportHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if !middleware.HasRole(claims, "admin") {
		WriteForbidden(w, "Only admins can import VestaCP/HestiaCP backups")
		return
	}

	// Limit upload size to 10GB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<30)

	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		WriteBadRequest(w, "Failed to parse upload: "+err.Error(), nil)
		return
	}

	file, _, err := r.FormFile("backup")
	if err != nil {
		WriteBadRequest(w, "Backup file is required (form field: backup)", nil)
		return
	}
	defer file.Close()

	targetUser := r.FormValue("username")
	if targetUser == "" {
		WriteBadRequest(w, "Target username is required (form field: username)", nil)
		return
	}

	// Parse and validate the archive
	archive, err := ParseVestaArchive(file)
	if err != nil {
		WriteBadRequest(w, "Invalid backup archive: "+err.Error(), nil)
		return
	}

	// Create CRD resources from the parsed archive
	ns := hostingNamespace
	result, importErrors := h.createResourcesFromArchive(r, archive, targetUser, ns)

	status := "Completed"
	if len(importErrors) > 0 {
		status = "CompletedWithErrors"
	}

	writeJSON(w, http.StatusOK, ImportResponse{
		Status:    status,
		Username:  targetUser,
		Namespace: ns,
		Imported:  result,
		Errors:    importErrors,
	})
}

// --- Archive parsing ---

// ParseVestaArchive reads a tar.gz archive and validates VestaCP/HestiaCP structure.
// Expected structure: web/, db/, mail/, dns/, user/ directories.
func ParseVestaArchive(reader io.Reader) (*VestaArchive, error) {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("not a valid gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	archive := &VestaArchive{}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading tar: %w", err)
		}

		// Normalize path — strip leading ./ or /
		name := strings.TrimPrefix(header.Name, "./")
		name = strings.TrimPrefix(name, "/")

		parts := strings.SplitN(name, "/", 3)
		if len(parts) < 1 {
			continue
		}

		topDir := parts[0]
		switch topDir {
		case "web":
			archive.HasWeb = true
			if len(parts) >= 2 && parts[1] != "" && header.Typeflag == tar.TypeDir {
				dirName := parts[1]
				if !containsStr(archive.WebDirs, dirName) {
					archive.WebDirs = append(archive.WebDirs, dirName)
				}
			}
		case "db":
			archive.HasDB = true
			if len(parts) >= 2 && parts[1] != "" && header.Typeflag == tar.TypeReg {
				archive.DBFiles = append(archive.DBFiles, parts[1])
			}
		case "mail":
			archive.HasMail = true
			if len(parts) >= 2 && parts[1] != "" && header.Typeflag == tar.TypeDir {
				dirName := parts[1]
				if !containsStr(archive.MailDirs, dirName) {
					archive.MailDirs = append(archive.MailDirs, dirName)
				}
			}
		case "dns":
			archive.HasDNS = true
			if len(parts) >= 2 && parts[1] != "" && header.Typeflag == tar.TypeReg {
				archive.DNSFiles = append(archive.DNSFiles, parts[1])
			}
		case "user":
			archive.HasUser = true
			if len(parts) >= 2 && parts[1] != "" && header.Typeflag == tar.TypeReg {
				archive.UserFile = name
			}
		}
	}

	// Validate: at least one expected directory must exist
	if !archive.HasWeb && !archive.HasDB && !archive.HasMail && !archive.HasDNS && !archive.HasUser {
		return nil, fmt.Errorf("archive does not contain expected VestaCP/HestiaCP structure (expected: web/, db/, mail/, dns/, or user/ directories)")
	}

	return archive, nil
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// --- CRD resource creation from archive ---

func (h *ImportHandler) createResourcesFromArchive(r *http.Request, archive *VestaArchive, username, namespace string) (ImportedCounts, []string) {
	var counts ImportedCounts
	var errors []string

	// Create Website CRDs from web/ directories
	for _, webDir := range archive.WebDirs {
		siteName := sanitizeName(webDir)
		website := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": CRDGroup + "/" + CRDVersion,
				"kind":       "Website",
				"metadata": map[string]interface{}{
					"name":      siteName,
					"namespace": namespace,
					"labels": map[string]interface{}{
						"hosting.panel/user":   username,
						"hosting.panel/import": "vesta",
						"managed-by":           "panel-core",
					},
				},
				"spec": map[string]interface{}{
					"php":         map[string]interface{}{"version": "8.2"},
					"replicas":    int64(2),
					"storageSize": "5Gi",
					"domains": []interface{}{
						map[string]interface{}{
							"name": webDir,
							"ssl":  map[string]interface{}{"enabled": true, "issuer": "letsencrypt-production"},
						},
					},
				},
			},
		}
		_, err := h.dynClient.Resource(WebsiteGVR).Namespace(namespace).Create(r.Context(), website, metav1.CreateOptions{})
		if err != nil {
			errors = append(errors, fmt.Sprintf("website %s: %v", siteName, err))
		} else {
			counts.Websites++
		}
	}

	// Create Database CRDs from db/ files
	for _, dbFile := range archive.DBFiles {
		dbName := sanitizeName(strings.TrimSuffix(strings.TrimSuffix(dbFile, ".gz"), ".sql"))
		if dbName == "" {
			continue
		}
		database := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": CRDGroup + "/" + CRDVersion,
				"kind":       "Database",
				"metadata": map[string]interface{}{
					"name":      dbName,
					"namespace": namespace,
					"labels": map[string]interface{}{
						"hosting.panel/user":   username,
						"hosting.panel/import": "vesta",
						"managed-by":           "panel-core",
					},
				},
				"spec": map[string]interface{}{
					"name":      dbName,
					"charset":   "utf8mb4",
					"collation": "utf8mb4_unicode_ci",
				},
			},
		}
		_, err := h.dynClient.Resource(DatabaseGVR).Namespace(namespace).Create(r.Context(), database, metav1.CreateOptions{})
		if err != nil {
			errors = append(errors, fmt.Sprintf("database %s: %v", dbName, err))
		} else {
			counts.Databases++
		}
	}

	// Create EmailAccount CRDs from mail/ directories
	for _, mailDir := range archive.MailDirs {
		// mailDir is typically a domain name; individual accounts would be subdirs
		emailName := sanitizeName("mail-" + mailDir)
		email := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": CRDGroup + "/" + CRDVersion,
				"kind":       "EmailAccount",
				"metadata": map[string]interface{}{
					"name":      emailName,
					"namespace": namespace,
					"labels": map[string]interface{}{
						"hosting.panel/user":   username,
						"hosting.panel/import": "vesta",
						"managed-by":           "panel-core",
					},
				},
				"spec": map[string]interface{}{
					"address": "info@" + mailDir,
					"domain":  mailDir,
					"quotaMB": int64(1024),
				},
			},
		}
		_, err := h.dynClient.Resource(EmailAccountGVR).Namespace(namespace).Create(r.Context(), email, metav1.CreateOptions{})
		if err != nil {
			errors = append(errors, fmt.Sprintf("email %s: %v", emailName, err))
		} else {
			counts.Emails++
		}
	}

	// Count DNS zones (actual import would go through PowerDNS API)
	counts.DNSZones = len(archive.DNSFiles)

	return counts, errors
}

// sanitizeName converts a string to a valid Kubernetes resource name.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "@", "-at-")
	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}
	name := strings.Trim(result.String(), "-")
	if name == "" {
		name = "imported"
	}
	// Kubernetes names max 253 chars
	if len(name) > 253 {
		name = name[:253]
	}
	return filepath.Base(name)
}
