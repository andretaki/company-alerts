// /home/andre/company-alerts/internal/config/config.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"slices" // Requires Go 1.21+
	"time"
	// No import of network package here
)

// Config holds the application's configuration settings.
type Config struct {
	ServerURL        string   `json:"server_url"`
	Token            string   `json:"token"`
	UseWebSocket     bool     `json:"use_websocket"`
	PollIntervalSecs int      `json:"poll_interval_seconds"`
	NotifyTypes      []string `json:"notify_types"` // Empty means allow all types
	MinSeverity      int      `json:"min_severity"`

	// Derived field for convenience
	PollInterval time.Duration `json:"-"`
}

// Load reads the configuration file from the given path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	// Basic validation and derived fields
	if c.ServerURL == "" {
		return nil, fmt.Errorf("config validation failed: server_url is required")
	}
	if c.Token == "" || c.Token == "REPLACE_ME" {
		return nil, fmt.Errorf("config validation failed: token is required (and should not be 'REPLACE_ME')")
	}
	if c.PollIntervalSecs <= 0 {
		c.PollIntervalSecs = 30 // Default if invalid
	}
	c.PollInterval = time.Duration(c.PollIntervalSecs) * time.Second

	if c.MinSeverity < 0 {
		c.MinSeverity = 0 // Ensure severity is non-negative
	}

	return &c, nil
}

// ShouldNotify determines if an alert meets the configured criteria for notification.
// Takes type and severity directly to avoid import cycle.
func (c *Config) ShouldNotify(alertType string, alertSeverity int) bool {
	// Check severity first
	if alertSeverity < c.MinSeverity {
		return false
	}

	// If NotifyTypes is empty, all types matching severity are allowed
	if len(c.NotifyTypes) == 0 {
		return true
	}

	// Check if the alert type is in the allowed list
	return slices.Contains(c.NotifyTypes, alertType)
}
