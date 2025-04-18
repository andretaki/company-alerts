// /home/andre/company-alerts/internal/config/config.go
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"slices" // Requires Go 1.21+
	"time"

	"github.com/yourorg/company-alerts/internal/model" // Import the new model package
)

const (
	// TokenEnvVar defines the environment variable name for the API token.
	TokenEnvVar = "ALERTS_TOKEN" // Changed from local var to const
)

// Config holds the application's configuration settings.
type Config struct {
	ServerURL        string   `json:"server_url"`
	Token            string   `json:"token"` // Might be overridden by env var
	UseWebSocket     bool     `json:"use_websocket"`
	PollIntervalSecs int      `json:"poll_interval_seconds"`
	NotifyTypes      []string `json:"notify_types"` // Empty means allow all types
	MinSeverity      int      `json:"min_severity"`

	// Derived field for convenience
	PollInterval time.Duration `json:"-"`
}

// Load reads the configuration file and applies environment variable overrides.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	// --- Token Handling ---
	// Prioritize environment variable for token
	envToken := os.Getenv(TokenEnvVar)
	if envToken != "" {
		log.Printf("Using token from environment variable %s", TokenEnvVar)
		c.Token = envToken
	} else {
		log.Printf("Using token from config file %s (Consider using %s env var for security)", path, TokenEnvVar)
		// Validate token from file if env var wasn't used
		if c.Token == "" || c.Token == "REPLACE_ME" {
			return nil, fmt.Errorf("config validation failed: token is required in %q or via %s env var (and should not be 'REPLACE_ME')", path, TokenEnvVar)
		}
	}
	// Final check after potential override
	if c.Token == "" {
		// Should not happen if validation above is correct, but defensive check.
		return nil, fmt.Errorf("config validation failed: token is missing")
	}

	// --- Basic validation and derived fields ---
	if c.ServerURL == "" {
		return nil, fmt.Errorf("config validation failed: server_url is required")
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
// Accepts model.Alert for potential future flexibility, though currently only uses type/severity.
func (c *Config) ShouldNotify(alert model.Alert) bool {
	// Check severity first
	if alert.Severity < c.MinSeverity {
		return false
	}

	// If NotifyTypes is empty, all types matching severity are allowed
	if len(c.NotifyTypes) == 0 {
		return true
	}

	// Check if the alert type is in the allowed list
	return slices.Contains(c.NotifyTypes, alert.Type)
}
