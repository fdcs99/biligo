package panelauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	password string
	ttl      time.Duration

	mu     sync.Mutex
	tokens map[string]time.Time
	now    func() time.Time
}

func NewManager(password string, ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Manager{
		password: strings.TrimSpace(password),
		ttl:      ttl,
		tokens:   map[string]time.Time{},
		now:      time.Now,
	}
}

func (m *Manager) Login(password string) (string, time.Time, bool, error) {
	if !m.passwordMatches(password) {
		return "", time.Time{}, false, nil
	}
	token, err := generateToken()
	if err != nil {
		return "", time.Time{}, false, err
	}
	expiresAt := m.now().Add(m.ttl)

	m.mu.Lock()
	m.pruneLocked(m.now())
	m.tokens[token] = expiresAt
	m.mu.Unlock()

	return token, expiresAt, true, nil
}

func (m *Manager) Validate(token string) (time.Time, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, false
	}

	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)

	expiresAt, ok := m.tokens[token]
	if !ok || !expiresAt.After(now) {
		delete(m.tokens, token)
		return time.Time{}, false
	}
	return expiresAt, true
}

func (m *Manager) passwordMatches(password string) bool {
	expected := []byte(m.password)
	actual := []byte(strings.TrimSpace(password))
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func (m *Manager) pruneLocked(now time.Time) {
	for token, expiresAt := range m.tokens {
		if !expiresAt.After(now) {
			delete(m.tokens, token)
		}
	}
}

func generateToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
