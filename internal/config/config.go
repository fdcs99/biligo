package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server                 ServerConfig   `yaml:"server"`
	Database               DatabaseConfig `yaml:"database"`
	Auth                   AuthConfig     `yaml:"auth"`
	Path                   string         `yaml:"-"`
	GeneratedPanelPassword string         `yaml:"-"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type AuthConfig struct {
	Password string `yaml:"password"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Database: DatabaseConfig{
			Path: "data/biligo.db",
		},
	}

	if path == "" {
		path = os.Getenv("BILIGO_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	cfg.Path = path

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
	} else if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	envPanelPassword := strings.TrimSpace(os.Getenv("BILIGO_PANEL_PASSWORD"))
	if envPanelPassword != "" {
		cfg.Auth.Password = envPanelPassword
	}

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = ":8080"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "data/biligo.db"
	}
	if strings.TrimSpace(cfg.Auth.Password) == "" {
		password, err := generatePanelPassword()
		if err != nil {
			return Config{}, err
		}
		cfg.Auth.Password = password
		cfg.GeneratedPanelPassword = password
		if err := writeConfig(path, cfg); err != nil {
			return Config{}, err
		}
	}
	if addr := os.Getenv("BILIGO_ADDR"); addr != "" {
		cfg.Server.Addr = addr
	}
	if dbPath := os.Getenv("BILIGO_DB"); dbPath != "" {
		cfg.Database.Path = dbPath
	}

	return cfg, nil
}

func generatePanelPassword() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func writeConfig(path string, cfg Config) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
