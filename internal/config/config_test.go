package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGeneratesAndWritesPanelPassword(t *testing.T) {
	t.Setenv("BILIGO_CONFIG", "")
	t.Setenv("BILIGO_PANEL_PASSWORD", "")
	t.Setenv("BILIGO_ADDR", ":9999")
	t.Setenv("BILIGO_DB", "env.db")

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Password == "" {
		t.Fatal("Auth.Password should be generated")
	}
	if cfg.GeneratedPanelPassword != cfg.Auth.Password {
		t.Fatalf("GeneratedPanelPassword = %q, want generated password", cfg.GeneratedPanelPassword)
	}
	if cfg.Server.Addr != ":9999" || cfg.Database.Path != "env.db" {
		t.Fatalf("env overrides were not applied: %#v", cfg)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "auth:") || !strings.Contains(string(data), cfg.Auth.Password) {
		t.Fatalf("generated config does not contain panel password:\n%s", string(data))
	}
	if strings.Contains(string(data), ":9999") || strings.Contains(string(data), "env.db") {
		t.Fatalf("runtime env overrides should not be written to config:\n%s", string(data))
	}
}

func TestLoadUsesPanelPasswordEnvWithoutWriting(t *testing.T) {
	t.Setenv("BILIGO_CONFIG", "")
	t.Setenv("BILIGO_PANEL_PASSWORD", "env-secret")

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Password != "env-secret" {
		t.Fatalf("Auth.Password = %q, want env-secret", cfg.Auth.Password)
	}
	if cfg.GeneratedPanelPassword != "" {
		t.Fatalf("GeneratedPanelPassword = %q, want empty", cfg.GeneratedPanelPassword)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config file err = %v, want not exist", err)
	}
}
