package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VaultPath       string      `yaml:"vault_path"`
	ServerPort      int         `yaml:"server_port"`
	BaseURL         string      `yaml:"base_url"`
	AnthropicAPIKey string      `yaml:"anthropic_api_key"`
	PebblePath      string      `yaml:"pebble_path"`
	ChatModel       string      `yaml:"chat_model"`
	DreamModel      string      `yaml:"dream_model"`
	Auth            AuthConfig      `yaml:"auth"`
	Dream           DreamConfig     `yaml:"dream"`
	DriveSync       DriveSyncConfig `yaml:"drive_sync"`
}

type AuthConfig struct {
	GoogleClientID     string `yaml:"google_client_id"`
	GoogleClientSecret string `yaml:"google_client_secret"`
	AllowedEmail       string `yaml:"allowed_email"`
	SessionSecret      string `yaml:"session_secret"`
}

type DreamConfig struct {
	Enabled      bool `yaml:"enabled"`
	StartHourUTC int  `yaml:"start_hour_utc"`
	EndHourUTC   int  `yaml:"end_hour_utc"`
}

type DriveSyncConfig struct {
	Enabled          bool   `yaml:"enabled"`
	VaultFolderName  string `yaml:"vault_folder_name"`
	PollIntervalSecs int    `yaml:"poll_interval_secs"`
	ConflictStrategy string `yaml:"conflict_strategy"`
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
	// Default BASE_URL to localhost so local dev works without extra config.
	if os.Getenv("BASE_URL") == "" {
		port := os.Getenv("PORT")
		os.Setenv("BASE_URL", "http://localhost:"+port)
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
	if c.ChatModel == "" {
		c.ChatModel = "claude-haiku-4-5-20251001"
	}
	if c.DreamModel == "" {
		c.DreamModel = "claude-sonnet-4-6"
	}
	if c.Dream.StartHourUTC == 0 {
		c.Dream.StartHourUTC = 19
	}
	if c.Dream.EndHourUTC == 0 {
		c.Dream.EndHourUTC = 23
	}
	if c.DriveSync.VaultFolderName == "" {
		c.DriveSync.VaultFolderName = "autowiki-vault"
	}
	if c.DriveSync.PollIntervalSecs == 0 {
		c.DriveSync.PollIntervalSecs = 60
	}
	if c.DriveSync.ConflictStrategy == "" {
		c.DriveSync.ConflictStrategy = "last_write_wins"
	}
	if c.VaultPath == "" {
		return fmt.Errorf("VAULT_PATH is required")
	}
	if c.PebblePath == "" {
		return fmt.Errorf("PEBBLE_PATH is required")
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
