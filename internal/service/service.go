// Package service holds Reelix's native application services.
//
// Services own business rules and transaction boundaries. They sit between the
// API layers and the repositories: handlers translate HTTP, repositories
// translate SQL, and neither contains policy.
//
// Nothing here knows about HTTP status codes or about Jellyfin. Both API
// surfaces map these errors to their own representations.
package service

import (
	"errors"
	"fmt"
)

var (
	// ErrAlreadySetUp means first-run setup was attempted on a server that
	// already has a user.
	ErrAlreadySetUp = errors.New("server is already set up")

	// ErrInvalidCredentials covers both an unknown username and a wrong
	// password. The two are deliberately not distinguished: telling a caller
	// which one failed turns the login endpoint into an account oracle.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrUnauthenticated means no valid token accompanied the request.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrForbidden means the caller is authenticated but not permitted.
	ErrForbidden = errors.New("forbidden")

	// ErrInvalidArgument means the caller supplied something unusable. The
	// accompanying message is safe to return.
	ErrInvalidArgument = errors.New("invalid argument")
)

// InvalidArgumentf builds an ErrInvalidArgument carrying a caller-safe reason.
func InvalidArgumentf(format string, args ...any) error {
	return &invalidArgument{msg: fmt.Sprintf(format, args...)}
}

type invalidArgument struct{ msg string }

func (e *invalidArgument) Error() string { return e.msg }
func (e *invalidArgument) Is(target error) bool {
	return target == ErrInvalidArgument
}
