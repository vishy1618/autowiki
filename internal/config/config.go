package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VaultPath      string     `yaml:"vault_path"`
	ServerPort     int        `yaml:"server_port"`
	AnthropicAPIKey string    `yaml:"anthropic_api_key"`
	RocksDBPath    string     `yaml:"rocksdb_path"`
	Auth           AuthConfig `yaml:"auth"`
	Dream          DreamConfig `yaml:"dream"`
}

type AuthConfig struct {
	GoogleClientID     string `yaml:"google_client_id"`
	GoogleClientSecret string `yaml:"google_client_secret"`
	AllowedEmail       string `yaml:"allowed_email"`
	SessionSecret      string `yaml:"session_secret"`
}

type DreamConfig struct {
	Enabled      bool `yaml:"enabled"`
	StartHourIST int  `yaml:"start_hour_ist"`
	EndHourIST   int  `yaml:"end_hour_ist"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Default SERVER_PORT to 8080 if not set so the YAML parses as a valid int.
	if os.Getenv("PORT") == "" {
		os.Setenv("PORT", "8080")
	}

	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	if c.VaultPath == "" {
		return fmt.Errorf("VAULT_PATH is required")
	}
	if c.RocksDBPath == "" {
		return fmt.Errorf("ROCKSDB_PATH is required")
	}
	if c.AnthropicAPIKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is required")
	}
	if c.Auth.GoogleClientID == "" {
		return fmt.Errorf("GOOGLE_CLIENT_ID is required")
	}
	if c.Auth.GoogleClientSecret == "" {
		return fmt.Errorf("GOOGLE_CLIENT_SECRET is required")
	}
	if c.Auth.AllowedEmail == "" {
		return fmt.Errorf("ALLOWED_EMAIL is required")
	}
	if c.Auth.SessionSecret == "" {
		return fmt.Errorf("SESSION_SECRET is required")
	}
	return nil
}
