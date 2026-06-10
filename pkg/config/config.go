package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds corral configuration.
type Config struct {
	Tailscale TailscaleConfig `yaml:"tailscale"`
}

// TailscaleConfig holds Tailscale-specific settings.
type TailscaleConfig struct {
	AuthKey string `yaml:"auth_key"`
}

// DefaultPath returns the default config file path.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tailvm", "config.yaml")
}

// Load reads the config file from path. Returns empty config if file doesn't exist.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// AuthKey returns the Tailscale auth key from config or the TS_AUTHKEY env var.
func AuthKey() string {
	// Check env var first
	if key := os.Getenv("TS_AUTHKEY"); key != "" {
		return key
	}
	// Fall back to config file
	cfg, err := Load("")
	if err == nil && cfg.Tailscale.AuthKey != "" {
		return cfg.Tailscale.AuthKey
	}
	return ""
}
