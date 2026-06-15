package panelauth

import (
	"testing"
	"time"
)

func TestManagerLoginValidateAndExpire(t *testing.T) {
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	manager := NewManager("secret", time.Hour)
	manager.now = func() time.Time { return now }

	if _, _, ok, err := manager.Login("wrong"); err != nil || ok {
		t.Fatalf("Login wrong ok = %v, err = %v", ok, err)
	}

	token, expiresAt, ok, err := manager.Login("secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !ok || token == "" {
		t.Fatalf("Login ok = %v, token = %q", ok, token)
	}
	if !expiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiresAt = %s, want %s", expiresAt, now.Add(time.Hour))
	}

	if got, ok := manager.Validate(token); !ok || !got.Equal(expiresAt) {
		t.Fatalf("Validate ok = %v, expiresAt = %s", ok, got)
	}

	now = now.Add(time.Hour + time.Second)
	if _, ok := manager.Validate(token); ok {
		t.Fatal("expired token should not validate")
	}
}

func TestManagerRevoke(t *testing.T) {
	manager := NewManager("secret", time.Hour)
	token, _, ok, err := manager.Login("secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !ok {
		t.Fatal("login failed")
	}
	if !manager.Revoke(token) {
		t.Fatal("Revoke should return true for active token")
	}
	if _, ok := manager.Validate(token); ok {
		t.Fatal("revoked token should not validate")
	}
}
