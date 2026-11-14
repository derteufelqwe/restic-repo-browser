package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memStore struct {
	hash     string
	sessions map[string]time.Time
}

func (m *memStore) PasswordHash(context.Context) (string, bool, error) {
	return m.hash, m.hash != "", nil
}
func (m *memStore) SetPasswordHash(_ context.Context, h string) error {
	m.hash, m.sessions = h, map[string]time.Time{}
	return nil
}
func (m *memStore) CreateSession(_ context.Context, h string, _, exp time.Time) error {
	m.sessions[h] = exp
	return nil
}
func (m *memStore) SessionValid(_ context.Context, h string, now time.Time) (bool, error) {
	exp, ok := m.sessions[h]
	return ok && exp.After(now), nil
}
func (m *memStore) DeleteSession(_ context.Context, h string) error {
	delete(m.sessions, h)
	return nil
}
func (m *memStore) PurgeSessions(context.Context, time.Time) error { return nil }

var ctx = context.Background()

func newService(t *testing.T) (*Service, *memStore, *time.Time) {
	t.Helper()
	st := &memStore{sessions: map[string]time.Time{}}
	s := New(st)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.SetPassword(ctx, "correct horse"); err != nil {
		t.Fatal(err)
	}
	return s, st, &now
}

func TestLoginAndSession(t *testing.T) {
	s, st, _ := newService(t)
	token, exp, err := s.Login(ctx, "correct horse", "1.2.3.4")
	if err != nil || token == "" {
		t.Fatalf("login: %q, %v", token, err)
	}
	if _, stored := st.sessions[token]; stored {
		t.Error("the raw token must not be stored")
	}
	if !exp.Equal(s.now().Add(SessionLifetime)) || !s.Valid(ctx, token) {
		t.Error("session should be valid")
	}
	if s.Valid(ctx, "garbage") || s.Valid(ctx, "") {
		t.Error("unknown tokens must be rejected")
	}
	_ = s.Logout(ctx, token)
	if s.Valid(ctx, token) {
		t.Error("session should be gone after logout")
	}

	token, _, _ = s.Login(ctx, "correct horse", "1.2.3.4")
	if err := s.SetPassword(ctx, "another password"); err != nil {
		t.Fatal(err)
	}
	if s.Valid(ctx, token) {
		t.Error("old session survived a password change")
	}
}

func TestWrongPasswordAndNotConfigured(t *testing.T) {
	s, _, _ := newService(t)
	if _, _, err := s.Login(ctx, "nope", "ip"); !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("err = %v", err)
	}
	fresh := New(&memStore{})
	if _, _, err := fresh.Login(ctx, "x", "ip"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v", err)
	}
	if err := fresh.SetPassword(ctx, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("err = %v", err)
	}
}

func TestThrottle(t *testing.T) {
	s, _, now := newService(t)
	for i := 0; i < maxFailures; i++ {
		if _, _, err := s.Login(ctx, "bad", "ip"); !errors.Is(err, ErrInvalidPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := s.Login(ctx, "correct horse", "ip"); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("blocked client: %v", err)
	}
	if _, _, err := s.Login(ctx, "correct horse", "other-ip"); err != nil {
		t.Errorf("other clients are not affected: %v", err)
	}
	*now = now.Add(lockout + time.Second)
	if _, _, err := s.Login(ctx, "correct horse", "ip"); err != nil {
		t.Errorf("after the lockout: %v", err)
	}
	if _, _, err := s.Login(ctx, "bad", "other-ip"); !errors.Is(err, ErrInvalidPassword) || len(s.failures) != 1 {
		t.Errorf("stale failures must be forgotten: %v, %d entries", err, len(s.failures))
	}
}
