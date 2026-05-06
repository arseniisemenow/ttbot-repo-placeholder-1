package s21

import (
	"context"
	"sync"
)

// Mock is a stub Client for tests. Authenticate looks up a (login → profile)
// table; LookupByLogin uses the same. Failure can be injected per method.
type Mock struct {
	mu             sync.Mutex
	Profiles       map[string]Profile // login → Profile
	AuthPasswords  map[string]string  // login → expected password (Authenticate)
	AdminPasswords map[string]string  // adminLogin → expected admin password (LookupByLogin)
	Failures       map[string]error
}

// NewMock returns a Mock with empty maps.
func NewMock() *Mock {
	return &Mock{
		Profiles:       map[string]Profile{},
		AuthPasswords:  map[string]string{},
		AdminPasswords: map[string]string{},
		Failures:       map[string]error{},
	}
}

// SetUser configures the mock so that login authenticates with `password` and
// resolves to `profile`.
func (m *Mock) SetUser(login, password string, profile Profile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if profile.Login == "" {
		profile.Login = login
	}
	m.Profiles[login] = profile
	m.AuthPasswords[login] = password
}

// SetAdminPassword sets the expected admin password used by LookupByLogin.
func (m *Mock) SetAdminPassword(login, password string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AdminPasswords[login] = password
}

// FailNext injects a one-shot failure for the given method.
func (m *Mock) FailNext(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Failures[method] = err
}

func (m *Mock) tryFail(method string) error {
	if err, ok := m.Failures[method]; ok {
		delete(m.Failures, method)
		return err
	}
	return nil
}

// Authenticate validates credentials.
func (m *Mock) Authenticate(ctx context.Context, login, password string) (Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.tryFail("Authenticate"); err != nil {
		return Profile{}, err
	}
	if want, ok := m.AuthPasswords[login]; !ok || want != password {
		return Profile{}, ErrInvalidCredentials
	}
	return m.Profiles[login], nil
}

// LookupByLogin resolves a profile using admin credentials.
func (m *Mock) LookupByLogin(ctx context.Context, adminLogin, adminPassword, targetLogin string) (Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.tryFail("LookupByLogin"); err != nil {
		return Profile{}, err
	}
	if want, ok := m.AdminPasswords[adminLogin]; !ok || want != adminPassword {
		return Profile{}, ErrInvalidCredentials
	}
	p, ok := m.Profiles[targetLogin]
	if !ok {
		return Profile{}, ErrNotFound
	}
	return p, nil
}
