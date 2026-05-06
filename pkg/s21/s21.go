// Package s21 wraps the S21 platform API. The bot only uses two operations:
// authenticating an admin (login + password) and resolving a nickname's
// campus/coalition information for verified registration.
package s21

import (
	"context"
	"errors"
)

// Profile is the subset of S21 user data the bot stores.
type Profile struct {
	Login         string
	CampusID      string
	CampusName    string
	CoalitionName string
}

// Client is the abstraction over s21auto-client-go (or any future S21 client).
// All methods take a context for cancellation/timeout.
type Client interface {
	// Authenticate validates a (login, password) pair and returns the resulting
	// user's profile. Must return ErrInvalidCredentials when the credentials
	// are wrong or the token is expired.
	Authenticate(ctx context.Context, login, password string) (Profile, error)

	// LookupByLogin returns the profile for a given login, using the bot's
	// stored admin credentials for authentication. Returns ErrNotFound when no
	// such login exists, ErrInvalidCredentials when the admin credentials
	// supplied are no longer valid (expired token).
	LookupByLogin(ctx context.Context, adminLogin, adminPassword, targetLogin string) (Profile, error)
}

// Errors returned by S21 clients.
var (
	ErrInvalidCredentials = errors.New("s21: invalid credentials")
	ErrNotFound           = errors.New("s21: not found")
	ErrUnavailable        = errors.New("s21: unavailable")
)
