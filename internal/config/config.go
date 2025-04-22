// internal/config/config.go
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/company-alerts/internal/model" // Use the unified model
)

// Config holds the client application's configuration settings.
type Config struct {
	ServerURL    string
	Token        string
	UseWebSocket bool
	PollInterval time.Duration
	NotifyTypes  []string // Empty means allow all types
	MinSeverity  int

	// Optional: HistoryFetchLimit int // How many alerts to fetch for history view
}

// Load loads configuration from environment variables.
func Load() (*Config, error) {
	c := Config{
		// Set defaults
		UseWebSocket: true,
		PollInterval: 30 * time.Second,
		MinSeverity:  1, // Default to show severity 1 and up
		// HistoryFetchLimit: 50, // Default history limit
	}
	var errs []string

	// Required: Server URL
	c.ServerURL = os.Getenv("ALERTS_SERVER_URL")
	if c.ServerURL == "" {
		errs = append(errs, "ALERTS_SERVER_URL environment variable is required")
	}

	// Required: Token
	c.Token = os.Getenv("ALERTS_CLIENT_TOKEN")
	if c.Token == "" {
		errs = append(errs, "ALERTS_CLIENT_TOKEN environment variable is required")
	}

	// Optional: Use WebSocket (default true)
	if wsEnv := os.Getenv("ALERTS_USE_WEBSOCKET"); wsEnv != "" {
		if val, err := strconv.ParseBool(wsEnv); err == nil {
			c.UseWebSocket = val
		} else {
			log.Printf("Warning: Invalid boolean value for ALERTS_USE_WEBSOCKET: '%s'. Using default: %t", wsEnv, c.UseWebSocket)
		}
	}

	// Optional: Poll Interval (default 30s)
	if pollEnv := os.Getenv("ALERTS_POLL_INTERVAL_SECONDS"); pollEnv != "" {
		if secs, err := strconv.Atoi(pollEnv); err == nil && secs > 0 {
			c.PollInterval = time.Duration(secs) * time.Second
		} else {
			log.Printf("Warning: Invalid integer value for ALERTS_POLL_INTERVAL_SECONDS: '%s'. Using default: %v", pollEnv, c.PollInterval)
		}
	}

	// Optional: Notify Types (comma-separated, default all)
	if typesEnv := os.Getenv("ALERTS_NOTIFY_TYPES"); typesEnv != "" {
		parts := strings.Split(typesEnv, ",")
		c.NotifyTypes = []string{} // Reset default if env var is set
		for _, t := range parts {
			trimmed := strings.TrimSpace(t)
			if trimmed != "" {
				c.NotifyTypes = append(c.NotifyTypes, trimmed)
			}
		}
	}

	// Optional: Min Severity (default 1)
	if sevEnv := os.Getenv("ALERTS_MIN_SEVERITY"); sevEnv != "" {
		if sev, err := strconv.Atoi(sevEnv); err == nil && sev >= 0 {
			c.MinSeverity = sev
		} else {
			log.Printf("Warning: Invalid integer value for ALERTS_MIN_SEVERITY: '%s'. Using default: %d", sevEnv, c.MinSeverity)
		}
	}

	// Optional: History Fetch Limit
	// if histEnv := os.Getenv("ALERTS_HISTORY_FETCH_LIMIT"); histEnv != "" {
	//  if limit, err := strconv.Atoi(histEnv); err == nil && limit > 0 && limit <= 1000 {
	//      c.HistoryFetchLimit = limit
	//  } else {
	//      log.Printf("Warning: Invalid integer value for ALERTS_HISTORY_FETCH_LIMIT: '%s'. Using default: %d", histEnv, c.HistoryFetchLimit)
	//  }
	// }

	if len(errs) > 0 {
		return nil, fmt.Errorf("configuration errors:\n- %s", strings.Join(errs, "\n- "))
	}

	// Note: Logging the loaded config is done in main.go after successful load

	return &c, nil
}

// ShouldNotify determines if an alert meets the configured criteria for notification.
func (c *Config) ShouldNotify(alert model.Alert) bool {
	// Check severity first
	if alert.Severity < c.MinSeverity {
		return false
	}

	// If NotifyTypes is empty or nil, all types matching severity are allowed
	if len(c.NotifyTypes) == 0 {
		return true
	}

	// Check if the alert type is in the allowed list (case-insensitive)
	for _, allowedType := range c.NotifyTypes {
		if strings.EqualFold(alert.Type, allowedType) {
			return true
		}
	}
	// Type not found in the allowed list
	return false
}
