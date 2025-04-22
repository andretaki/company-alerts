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
		log.Printf("INFO: PORT environment variable not set, using default %s", port)
	}

	// Load allowed tokens from a comma-separated env var
	// Example: export ALLOWED_TOKENS="token1,token2,token3"
	tokenString := os.Getenv("ALLOWED_TOKENS")
	if tokenString == "" {
		log.Println("WARNING: ALLOWED_TOKENS environment variable is not set or empty. No clients or API calls can authenticate.")
		// Optionally return error if tokens are mandatory:
		// return nil, errors.New("ALLOWED_TOKENS environment variable is required and cannot be empty")
	}

	tokens := make(map[string]bool)
	count := 0
	if tokenString != "" {
		parts := strings.Split(tokenString, ",")
		for _, token := range parts {
			trimmed := strings.TrimSpace(token)
			if trimmed != "" {
				if _, exists := tokens[trimmed]; !exists {
					tokens[trimmed] = true
					// Avoid logging full tokens even here, maybe log hash or just count
					// log.Printf("DEBUG: Loaded allowed token: %s...", trimmed[:min(len(trimmed), 4)]) // Log prefix only
					count++
				} else {
					log.Printf("WARNING: Duplicate token found in ALLOWED_TOKENS: %s...", trimmed[:min(len(trimmed), 4)])
				}
			}
		}
	}

	if count == 0 && tokenString != "" {
		log.Println("WARNING: ALLOWED_TOKENS was set but contained no valid, non-empty tokens.")
	} else if count > 0 {
		log.Printf("INFO: Loaded %d unique allowed token(s).", count)
	}
	// If tokenString was empty initially, the earlier warning suffices.

	// Validate port number format if needed
	// _, err := strconv.Atoi(port)
	// if err != nil {
	//     return nil, fmt.Errorf("invalid PORT value: %s", port)
	// }

	cfg := &ServerConfig{
		Port:          port,
		AllowedTokens: tokens,
	}

	return cfg, nil
}

// IsTokenAllowed checks if a given token is in the allowed list.
// Performs a case-sensitive lookup.
func (c *ServerConfig) IsTokenAllowed(token string) bool {
	// Ensure comparison is done on non-empty token
	if token == "" {
		return false
	}
	_, ok := c.AllowedTokens[token]
	return ok
}

// Helper for logging token prefix (use cautiously)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
