// Package auth implements the single-user, password-only login: bcrypt-hashed
// password, random session tokens (stored only as hashes) and throttling of
// repeated wrong passwords.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Errors returned by Login and SetPassword.
var (
	ErrInvalidPassword  = errors.New("wrong password")
	ErrNotConfigured    = errors.New("no login password has been set")
	ErrTooManyAttempts  = errors.New("too many wrong passwords, try again later")
	ErrPasswordTooShort = errors.New("the password must have at least 8 characters")
)

// Tuning knobs.
const (
	SessionLifetime = 30 * 24 * time.Hour
	minPasswordLen  = 8
	maxFailures     = 5
	lockout         = time.Minute
)

// Store persists the password hash and sessions.
type Store interface {
	PasswordHash(ctx context.Context) (hash string, ok bool, err error)
	SetPasswordHash(ctx context.Context, hash string) error
	CreateSession(ctx context.Context, tokenHash string, now, expires time.Time) error
	SessionValid(ctx context.Context, tokenHash string, now time.Time) (bool, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	PurgeSessions(ctx context.Context, now time.Time) error
}

// Service performs login, logout and session checks.
type Service struct {
	store Store
	now   func() time.Time

	mu       sync.Mutex
	failures map[string]*failure
}

type failure struct {
	count int
	last  time.Time // the latest wrong password; failures older than the lockout are forgotten
	until time.Time // once count reached maxFailures: no attempts before this
}

// New creates a Service.
func New(store Store) *Service {
	return &Service{store: store, now: time.Now, failures: map[string]*failure{}}
}

// HasPassword reports whether a login password has been configured.
func (s *Service) HasPassword(ctx context.Context) (bool, error) {
	_, ok, err := s.store.PasswordHash(ctx)
	return ok, err
}

// SetPassword replaces the login password and ends all sessions.
func (s *Service) SetPassword(ctx context.Context, password string) error {
	if len([]rune(password)) < minPasswordLen {
		return ErrPasswordTooShort
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}
	return s.store.SetPasswordHash(ctx, string(hash))
}

// Login checks the password and starts a session. clientKey (an IP address)
// identifies who is throttled after repeated failures.
func (s *Service) Login(ctx context.Context, password, clientKey string) (token string, expires time.Time, err error) {
	if err := s.checkThrottle(clientKey); err != nil {
		return "", time.Time{}, err
	}
	hash, ok, err := s.store.PasswordHash(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	if !ok {
		return "", time.Time{}, ErrNotConfigured
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.recordFailure(clientKey)
		return "", time.Time{}, ErrInvalidPassword
	}
	s.clearFailures(clientKey)

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires = now.Add(SessionLifetime)
	if err := s.store.CreateSession(ctx, hashToken(token), now, expires); err != nil {
		return "", time.Time{}, err
	}
	_ = s.store.PurgeSessions(ctx, now) // housekeeping; failure is harmless
	return token, expires, nil
}

// Valid reports whether token belongs to a live session.
func (s *Service) Valid(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	ok, err := s.store.SessionValid(ctx, hashToken(token), s.now())
	return err == nil && ok
}

// Logout ends the session of token.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashToken(token))
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) checkThrottle(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.failures[key]; f != nil && f.count >= maxFailures {
		if s.now().Before(f.until) {
			return ErrTooManyAttempts
		}
		delete(s.failures, key) // lockout is over
	}
	return nil
}

func (s *Service) recordFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, f := range s.failures { // keeps the map from growing with every client that ever failed
		if now.Sub(f.last) > lockout && now.After(f.until) {
			delete(s.failures, k)
		}
	}
	f := s.failures[key]
	if f == nil {
		f = &failure{}
		s.failures[key] = f
	}
	f.count++
	f.last = now
	if f.count >= maxFailures {
		f.until = now.Add(lockout)
	}
}

func (s *Service) clearFailures(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, key)
}
