// Package apperr is the error type every service returns. The HTTP layer turns it into the
// contract's Error body; anything else becomes a 500 "internal".
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
	Details   map[string]any
	cause     error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause keeps the underlying error for logs without exposing it to clients.
func (e *Error) WithCause(err error) *Error {
	c := *e
	c.cause = err
	return &c
}

func (e *Error) WithDetails(d map[string]any) *Error {
	c := *e
	c.Details = d
	return &c
}

func New(status int, code, message string, retryable bool) *Error {
	return &Error{Status: status, Code: code, Message: message, Retryable: retryable}
}

func Unauthenticated(msg string) *Error {
	return New(http.StatusUnauthorized, "unauthenticated", msg, false)
}
func Forbidden(msg string) *Error { return New(http.StatusForbidden, "forbidden", msg, false) }
func NotFound(msg string) *Error  { return New(http.StatusNotFound, "not_found", msg, false) }
func Invalid(msg string) *Error   { return New(http.StatusBadRequest, "invalid_argument", msg, false) }
func Conflict(msg string) *Error  { return New(http.StatusConflict, "conflict", msg, false) }
func RateLimited(msg string) *Error {
	return New(http.StatusTooManyRequests, "rate_limited", msg, true)
}
func Unavailable(msg string) *Error {
	return New(http.StatusServiceUnavailable, "unavailable", msg, true)
}
func NotImplemented() *Error {
	return New(http.StatusNotImplemented, "unavailable", "该功能尚未开放", false)
}
func QuotaExceeded(msg string) *Error { return New(http.StatusForbidden, "quota_exceeded", msg, false) }
func ContentBlocked() *Error {
	return New(http.StatusUnprocessableEntity, "content_blocked", "内容未通过安全审核，请修改后重试", false)
}

// Internal wraps an unexpected error; clients only see a generic message.
func Internal(err error) *Error {
	return New(http.StatusInternalServerError, "internal", "服务器开小差了，请稍后再试", true).WithCause(err)
}

// As returns err as *Error, wrapping unknown errors as Internal.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}
