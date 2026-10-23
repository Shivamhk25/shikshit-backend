package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode represents an application error code
type ErrorCode string

const (
	// Generic errors
	ErrCodeInternal       ErrorCode = "INTERNAL_ERROR"
	ErrCodeNotFound       ErrorCode = "NOT_FOUND"
	ErrCodeValidation     ErrorCode = "VALIDATION_ERROR"
	ErrCodeUnauthorized   ErrorCode = "UNAUTHORIZED"
	ErrCodeForbidden      ErrorCode = "FORBIDDEN"
	ErrCodeConflict       ErrorCode = "CONFLICT"
	ErrCodeRateLimited    ErrorCode = "RATE_LIMITED"
	ErrCodeBadRequest     ErrorCode = "BAD_REQUEST"

	// Auth errors
	ErrCodeInvalidCredentials ErrorCode = "INVALID_CREDENTIALS"
	ErrCodeTokenExpired       ErrorCode = "TOKEN_EXPIRED"
	ErrCodeTokenInvalid       ErrorCode = "TOKEN_INVALID"
	ErrCodeTokenRevoked       ErrorCode = "TOKEN_REVOKED"
	ErrCodeRefreshExpired     ErrorCode = "REFRESH_TOKEN_EXPIRED"
	ErrCodeRefreshRevoked     ErrorCode = "REFRESH_TOKEN_REVOKED"
	ErrCodeCaptchaRequired    ErrorCode = "CAPTCHA_REQUIRED"
	ErrCodeCaptchaInvalid     ErrorCode = "CAPTCHA_INVALID"

	// Tenant errors
	ErrCodeTenantNotFound     ErrorCode = "TENANT_NOT_FOUND"
	ErrCodeTenantInactive     ErrorCode = "TENANT_INACTIVE"
	ErrCodeTenantSuspended    ErrorCode = "TENANT_SUSPENDED"
	ErrCodeCrossTenantAccess  ErrorCode = "CROSS_TENANT_ACCESS"

	// Resource errors
	ErrCodeResourceExists     ErrorCode = "RESOURCE_EXISTS"
	ErrCodeResourceInUse      ErrorCode = "RESOURCE_IN_USE"
	ErrCodeResourceLocked     ErrorCode = "RESOURCE_LOCKED"

	// Business logic errors
	ErrCodeEnrollmentFull     ErrorCode = "ENROLLMENT_FULL"
	ErrCodePrerequisiteMissing ErrorCode = "PREREQUISITE_MISSING"
	ErrCodeScheduleConflict   ErrorCode = "SCHEDULE_CONFLICT"
	ErrCodeGradeLocked        ErrorCode = "GRADE_LOCKED"
)

// AppError represents an application error with code and details
type AppError struct {
	Code       ErrorCode              `json:"code"`
	Message    string                 `json:"message"`
	Details    map[string]interface{} `json:"details,omitempty"`
	HTTPStatus int                    `json:"-"`
	Cause      error                  `json:"-"`
}

// Error implements the error interface
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying error
func (e *AppError) Unwrap() error {
	return e.Cause
}

// WithDetail adds a detail to the error
func (e *AppError) WithDetail(key string, value interface{}) *AppError {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithCause sets the underlying cause
func (e *AppError) WithCause(err error) *AppError {
	e.Cause = err
	return e
}

// New creates a new AppError
func New(code ErrorCode, message string, httpStatus int) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
	}
}

// NewInternal creates an internal server error
func NewInternal(message string, cause error) *AppError {
	return &AppError{
		Code:       ErrCodeInternal,
		Message:    message,
		HTTPStatus: http.StatusInternalServerError,
		Cause:      cause,
	}
}

// NewNotFound creates a not found error
func NewNotFound(resource string) *AppError {
	return &AppError{
		Code:       ErrCodeNotFound,
		Message:    fmt.Sprintf("%s not found", resource),
		HTTPStatus: http.StatusNotFound,
	}
}

// NewValidation creates a validation error
func NewValidation(message string, details map[string]interface{}) *AppError {
	err := &AppError{
		Code:       ErrCodeValidation,
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
	if details != nil {
		err.Details = details
	}
	return err
}

// NewUnauthorized creates an unauthorized error
func NewUnauthorized(message string) *AppError {
	if message == "" {
		message = "Authentication required"
	}
	return &AppError{
		Code:       ErrCodeUnauthorized,
		Message:    message,
		HTTPStatus: http.StatusUnauthorized,
	}
}

// NewForbidden creates a forbidden error
func NewForbidden(message string) *AppError {
	if message == "" {
		message = "Access denied"
	}
	return &AppError{
		Code:       ErrCodeForbidden,
		Message:    message,
		HTTPStatus: http.StatusForbidden,
	}
}

// NewConflict creates a conflict error
func NewConflict(message string) *AppError {
	return &AppError{
		Code:       ErrCodeConflict,
		Message:    message,
		HTTPStatus: http.StatusConflict,
	}
}

// NewRateLimited creates a rate limited error
func NewRateLimited(retryAfter int) *AppError {
	return &AppError{
		Code:       ErrCodeRateLimited,
		Message:    "Too many requests",
		HTTPStatus: http.StatusTooManyRequests,
		Details:    map[string]interface{}{"retry_after": retryAfter},
	}
}

// IsCode checks if an error has a specific code
func IsCode(err error, code ErrorCode) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == code
	}
	return false
}

// IsNotFound checks if error is not found
func IsNotFound(err error) bool {
	return IsCode(err, ErrCodeNotFound)
}

// IsUnauthorized checks if error is unauthorized
func IsUnauthorized(err error) bool {
	return IsCode(err, ErrCodeUnauthorized)
}

// IsForbidden checks if error is forbidden
func IsForbidden(err error) bool {
	return IsCode(err, ErrCodeForbidden)
}

// IsValidation checks if error is validation
func IsValidation(err error) bool {
	return IsCode(err, ErrCodeValidation)
}

// IsConflict checks if error is conflict
func IsConflict(err error) bool {
	return IsCode(err, ErrCodeConflict)
}

// IsRateLimited checks if error is rate limited
func IsRateLimited(err error) bool {
	return IsCode(err, ErrCodeRateLimited)
}

// HTTPStatus returns the HTTP status code for an error
func HTTPStatus(err error) int {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.HTTPStatus
	}
	return http.StatusInternalServerError
}