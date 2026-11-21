package domain

import (
	"errors"
	"net/http"
)

// Error codes shared by every interface (spec 02 §11).
const (
	CodeValidation       = "validation_failed"
	CodeImmutable        = "immutable_field"
	CodeSecret           = "secret_detected"
	CodeVersionConflict  = "version_conflict"
	CodePrecondition     = "precondition_required"
	CodeInvalidTransition = "invalid_transition"
	CodeUnauthenticated  = "unauthenticated"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not_found"
	CodeProjectRequired  = "project_required"
	CodeProjectAmbiguous = "project_ambiguous"
	CodeProjectExists    = "project_exists"
	CodeRepositoryTaken  = "repository_taken"
	CodeIdempotency      = "idempotency_mismatch"
)

var httpStatus = map[string]int{
	CodeValidation:        http.StatusUnprocessableEntity,
	CodeImmutable:         http.StatusUnprocessableEntity,
	CodeSecret:            http.StatusUnprocessableEntity,
	CodeVersionConflict:   http.StatusPreconditionFailed,
	CodePrecondition:      http.StatusPreconditionRequired,
	CodeInvalidTransition: http.StatusConflict,
	CodeUnauthenticated:   http.StatusUnauthorized,
	CodeForbidden:         http.StatusForbidden,
	CodeNotFound:          http.StatusNotFound,
	CodeProjectRequired:   http.StatusBadRequest,
	CodeProjectAmbiguous:  http.StatusConflict,
	CodeProjectExists:     http.StatusConflict,
	CodeRepositoryTaken:   http.StatusConflict,
	CodeIdempotency:       http.StatusUnprocessableEntity,
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a business error with a stable code.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Fields  []FieldError   `json:"errors,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func (e *Error) HTTPStatus() int {
	if s, ok := httpStatus[e.Code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func Errf(code, msg string) *Error { return &Error{Code: code, Message: msg} }

func NotFound(what string) *Error { return Errf(CodeNotFound, what+" not found") }

// AsError extracts a *Error from err.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
