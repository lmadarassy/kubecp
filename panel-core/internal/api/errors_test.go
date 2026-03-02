package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		code       ErrorCode
		message    string
		details    interface{}
		wantStatus int
		wantCode   ErrorCode
	}{
		{
			name:       "bad request",
			status:     http.StatusBadRequest,
			code:       ErrCodeBadRequest,
			message:    "invalid input",
			details:    nil,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrCodeBadRequest,
		},
		{
			name:       "quota exceeded with details",
			status:     http.StatusUnprocessableEntity,
			code:       ErrCodeQuotaExceeded,
			message:    "website limit reached",
			details:    map[string]interface{}{"current": 5, "limit": 5},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   ErrCodeQuotaExceeded,
		},
		{
			name:       "internal error",
			status:     http.StatusInternalServerError,
			code:       ErrCodeInternalError,
			message:    "unexpected failure",
			details:    nil,
			wantStatus: http.StatusInternalServerError,
			wantCode:   ErrCodeInternalError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteError(w, tt.status, tt.code, tt.message, tt.details)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var resp APIError
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if resp.Error.Code != tt.wantCode {
				t.Errorf("error code = %q, want %q", resp.Error.Code, tt.wantCode)
			}
			if resp.Error.Message != tt.message {
				t.Errorf("error message = %q, want %q", resp.Error.Message, tt.message)
			}
		})
	}
}

func TestConvenienceHelpers(t *testing.T) {
	helpers := []struct {
		name       string
		fn         func(http.ResponseWriter)
		wantStatus int
		wantCode   ErrorCode
	}{
		{"WriteUnauthorized", func(w http.ResponseWriter) { WriteUnauthorized(w, "no token") }, 401, ErrCodeUnauthorized},
		{"WriteForbidden", func(w http.ResponseWriter) { WriteForbidden(w, "no access") }, 403, ErrCodeForbidden},
		{"WriteNotFound", func(w http.ResponseWriter) { WriteNotFound(w, "missing") }, 404, ErrCodeNotFound},
		{"WriteInternalError", func(w http.ResponseWriter) { WriteInternalError(w, "oops") }, 500, ErrCodeInternalError},
		{"WriteServiceUnavailable", func(w http.ResponseWriter) { WriteServiceUnavailable(w, "down") }, 503, ErrCodeServiceUnavail},
	}

	for _, h := range helpers {
		t.Run(h.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.fn(w)

			if w.Code != h.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, h.wantStatus)
			}

			var resp APIError
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("failed to decode: %v", err)
			}
			if resp.Error.Code != h.wantCode {
				t.Errorf("code = %q, want %q", resp.Error.Code, h.wantCode)
			}
		})
	}
}
