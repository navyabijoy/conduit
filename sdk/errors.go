package sdk

import (
	"errors"
	"net"
	"syscall"
)

// ErrorType classifies Conduit execution errors.
type ErrorType string

const (
	ErrorTypeTransient ErrorType = "transient"
	ErrorTypePermanent ErrorType = "permanent"
)

// ConduitError represents a structured framework error.
type ConduitError struct {
	Type    ErrorType
	Message string
	Code    int // Optional HTTP status code
	Err     error
}

func (e *ConduitError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *ConduitError) Unwrap() error {
	return e.Err
}

// NewTransientError creates a transient (retryable) error.
func NewTransientError(msg string, code int, err error) error {
	return &ConduitError{Type: ErrorTypeTransient, Message: msg, Code: code, Err: err}
}

// NewPermanentError creates a permanent (non-retryable) error.
func NewPermanentError(msg string, code int, err error) error {
	return &ConduitError{Type: ErrorTypePermanent, Message: msg, Code: code, Err: err}
}

// IsTransient checks if the error is classified as transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var ce *ConduitError
	if errors.As(err, &ce) {
		return ce.Type == ErrorTypeTransient
	}
	// Check standard net/sys errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	return false
}

// IsPermanent checks if the error is classified as permanent.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}
	return !IsTransient(err)
}
