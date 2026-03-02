package api

import (
	"encoding/json"
	"net/http"
)

// ErrorCode represents a machine-readable error code.
type ErrorCode string

const (
	ErrCodeBadRequest       ErrorCode = "BAD_REQUEST"
	ErrCodeUnauthorized     ErrorCode = "UNAUTHORIZED"
	ErrCodeForbidden        ErrorCode = "FORBIDDEN"
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeConflict         ErrorCode = "CONFLICT"
	ErrCodeQuotaExceeded    ErrorCode = "QUOTA_EXCEEDED"
	ErrCodeValidationFailed ErrorCode = "VALIDATION_FAILED"
	ErrCodeInternalError    ErrorCode = "INTERNAL_ERROR"
	ErrCodeServiceUnavail   ErrorCode = "SERVICE_UNAVAILABLE"
)

// APIError is the structured error response per the design doc.
type APIError struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error code, message, and optional details.
type ErrorDetail struct {
	Code    ErrorCode   `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// WriteError writes a structured JSON error response.
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string, details interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIError{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}

// Convenience helpers for common error responses.

func WriteBadRequest(w http.ResponseWriter, message string, details interface{}) {
	WriteError(w, http.StatusBadRequest, ErrCodeBadRequest, message, details)
}

func WriteUnauthorized(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusUnauthorized, ErrCodeUnauthorized, message, nil)
}

func WriteForbidden(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusForbidden, ErrCodeForbidden, message, nil)
}

func WriteNotFound(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusNotFound, ErrCodeNotFound, message, nil)
}

func WriteConflict(w http.ResponseWriter, message string, details interface{}) {
	WriteError(w, http.StatusConflict, ErrCodeConflict, message, details)
}

func WriteQuotaExceeded(w http.ResponseWriter, message string, details interface{}) {
	WriteError(w, http.StatusUnprocessableEntity, ErrCodeQuotaExceeded, message, details)
}

func WriteValidationFailed(w http.ResponseWriter, message string, details interface{}) {
	WriteError(w, http.StatusUnprocessableEntity, ErrCodeValidationFailed, message, details)
}

func WriteInternalError(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusInternalServerError, ErrCodeInternalError, message, nil)
}

func WriteServiceUnavailable(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail, message, nil)
}
