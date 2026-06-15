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
	Logging                LoggingConfig  `yaml:"logging"`
	Path                   string         `yaml:"-"`
	GeneratedConfigFile    bool           `yaml:"-"`
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

type LoggingConfig struct {
	Levels LogLevels `yaml:"levels"`
	Color  string    `yaml:"color"`
}

type LogLevels []string

func Load(path string) (Config, error) {
	cfg := Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Database: DatabaseConfig{
			Path: "data/biligo.db",
		},
		Logging: LoggingConfig{
			Levels: LogLevels{"info", "warn", "error"},
			Color:  "auto",
		},
	}

	if path == "" {
		path = os.Getenv("BILIGO_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	cfg.Path = path

	configFileExists := true
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		configFileExists = false
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
	cfg.Logging.Color = normalizeLogColor(cfg.Logging.Color)
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
		cfg.GeneratedConfigFile = !configFileExists
	}
	if addr := os.Getenv("BILIGO_ADDR"); addr != "" {
		cfg.Server.Addr = addr
	}
	if dbPath := os.Getenv("BILIGO_DB"); dbPath != "" {
		cfg.Database.Path = dbPath
	}
	if levels := strings.TrimSpace(os.Getenv("BILIGO_LOG_LEVELS")); levels != "" {
		cfg.Logging.Levels = parseLogLevels(levels)
	}
	if color := strings.TrimSpace(os.Getenv("BILIGO_LOG_COLOR")); color != "" {
		cfg.Logging.Color = normalizeLogColor(color)
	}

	return cfg, nil
}

func (levels *LogLevels) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*levels = parseLogLevels(value.Value)
	case yaml.SequenceNode:
		items := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			items = append(items, strings.TrimSpace(item.Value))
		}
		*levels = items
	}
	return nil
}

func parseLogLevels(raw string) LogLevels {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
	levels := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			levels = append(levels, part)
		}
	}
	return levels
}

func normalizeLogColor(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "auto"
	case "always", "on", "true", "force":
		return "always"
	case "never", "off", "false", "none":
		return "never"
	default:
		return "auto"
	}
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
