package config

import (
	"log"
	"os"
	"strings"
)

// ServerConfig holds the server's configuration settings.
type ServerConfig struct {
	Port          string
	AllowedTokens map[string]bool // Use a map for quick token lookup
}

// LoadServerConfig loads configuration from environment variables.
func LoadServerConfig() (*ServerConfig, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Default port
		log.Printf("PORT environment variable not set, using default %s", port)
	}

	// Load allowed tokens from a comma-separated env var
	// Example: export ALLOWED_TOKENS="token1,token2,token3"
	tokenString := os.Getenv("ALLOWED_TOKENS")
	if tokenString == "" {
		log.Println("WARNING: ALLOWED_TOKENS environment variable is not set. No clients will be able to authenticate.")
		// return nil, errors.New("ALLOWED_TOKENS environment variable is required")
	}

	tokens := make(map[string]bool)
	if tokenString != "" {
		parts := strings.Split(tokenString, ",")
		for _, token := range parts {
			trimmed := strings.TrimSpace(token)
			if trimmed != "" {
				tokens[trimmed] = true
				log.Printf("Loaded allowed token: %s...", trimmed[:min(len(trimmed), 4)]) // Log prefix only
			}
		}
	}
	if len(tokens) == 0 {
		log.Println("WARNING: No valid tokens loaded from ALLOWED_TOKENS.")
	}

	cfg := &ServerConfig{
		Port:          port,
		AllowedTokens: tokens,
	}

	return cfg, nil
}

// IsTokenAllowed checks if a given token is in the allowed list.
func (c *ServerConfig) IsTokenAllowed(token string) bool {
	_, ok := c.AllowedTokens[token]
	return ok
}

// Helper for logging token prefix
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
