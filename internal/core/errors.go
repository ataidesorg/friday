package core

import "errors"

// Sentinel errors shared by every port. Adapters wrap these with context so
// callers can branch with errors.Is.
var (
	ErrNotImplemented = errors.New("not implemented")
	ErrInvalidInput   = errors.New("invalid input")
	ErrSecretContent  = errors.New("secret content refused")
	ErrPolicyDenied   = errors.New("policy denied")
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
	ErrUnavailable    = errors.New("unavailable")
	ErrBudgetExceeded = errors.New("budget exceeded")
	ErrTimeout        = errors.New("timeout")
)

// NotImplementedError names a feature that is not built, so an honest "no"
// replaces fake behaviour in critical paths.
type NotImplementedError struct {
	Feature string
}

func (e NotImplementedError) Error() string {
	return e.Feature + ": not implemented"
}

// Is makes errors.Is(err, ErrNotImplemented) true.
func (e NotImplementedError) Is(target error) bool { return target == ErrNotImplemented }
