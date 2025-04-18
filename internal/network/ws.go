// /home/andre/company-alerts/internal/network/ws.go
// NOTE: This file now contains BOTH WebSocket and HTTP polling logic.
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand" // For jitter
	"net/http"
	"net/url"

	// "strconv" // No longer needed after Alert.String() change

	"time"

	// These should be resolved by `go mod tidy`
	"github.com/gorilla/websocket"
	"github.com/yourorg/company-alerts/internal/config" // Use concrete config type
	"github.com/yourorg/company-alerts/internal/model"  // Use the central model
)

// Alert defines the structure of an incoming alert.
type Alert struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Severity  int    `json:"severity"`
	Timestamp int64  `json:"timestamp"` // Unix Milliseconds
	Summary   string `json:"summary"`
	Detail    string `json:"detail"`
}

// Time returns the timestamp as a time.Time object.
func (a Alert) Time() time.Time {
	return time.UnixMilli(a.Timestamp)
}

// String provides a concise string representation of the alert.
func (a Alert) String() string {
	return fmt.Sprintf("[%s][Sev:%d] %s", a.Type, a.Severity, a.Summary)
}

// Listen starts the appropriate network listener (WebSocket or HTTP Polling)
func Listen(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	mode := "HTTP Polling"
	if cfg.UseWebSocket {
		mode = "WebSocket"
		go listenWS(ctx, cfg, out)
	} else {
		go pollHTTP(ctx, cfg, out)
	}
	log.Printf("Network listener starting. Mode: %s", mode)
}

// listenWS connects via WebSocket and listens for alerts. Handles reconnection.
func listenWS(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	// SECURITY WARNING: Token is passed in the URL query parameter.
	// While common for WebSockets, this can be logged by intermediaries.
	// Ensure the server-side endpoint is appropriately secured (e.g., HTTPS/WSS).
	// If higher security is needed, investigate token exchange mechanisms after connection establishment.
	wsURL, err := buildWebSocketURL(cfg.ServerURL, cfg.Token)
	if err != nil {
		log.Printf("Error building WebSocket URL: %v. WebSocket listener will not start.", err)
		return // Consider more robust error propagation? For now, log and exit goroutine.
	}
	log.Printf("Connecting to WebSocket: %s", wsURL.Redacted()) // Redacted() hides query params in log

	reconnectAttempts := 0
	maxReconnectInterval := 1 * time.Minute

	for {
		// Check context before attempting connection
		select {
		case <-ctx.Done():
			log.Println("WebSocket listener stopping before connect attempt due to context cancellation.")
			return
		default:
		}

		// Use a dialer with a timeout
		dialer := websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 15 * time.Second, // Added timeout
		}
		conn, resp, err := dialer.DialContext(ctx, wsURL.String(), nil) // Pass context to DialContext

		if err != nil {
			// Check if the error is due to context cancellation during dial
			if ctx.Err() != nil {
				log.Printf("WebSocket dial cancelled by context: %v", ctx.Err())
				return
			}

			reconnectAttempts++
			delay := calculateBackoff(reconnectAttempts, maxReconnectInterval)
			status := "N/A"
			if resp != nil {
				status = resp.Status
			}
			log.Printf("WebSocket dial error (status: %s): %v. Reconnecting attempt %d in %v...", status, err, reconnectAttempts, delay)

			// Wait for backoff duration or context cancellation
			select {
			case <-time.After(delay):
				continue // Try to reconnect
			case <-ctx.Done():
				log.Println("WebSocket listener stopping during reconnect backoff due to context cancellation.")
				return
			}
		}

		// Close response body defensively
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		log.Println("WebSocket connection established.")
		reconnectAttempts = 0      // Reset attempts on successful connection
		wsReadLoop(ctx, conn, out) // This loop runs until connection breaks or ctx is done
		log.Println("WebSocket connection closed or read loop exited. Attempting to reconnect...")

		// Check context again before attempting immediate reconnect delay
		select {
		case <-ctx.Done():
			log.Println("WebSocket listener stopping after connection close due to context cancellation.")
			return
		case <-time.After(1 * time.Second): // Small delay before immediate reconnect attempt
		}
	}
}

// wsReadLoop continuously reads messages from the WebSocket connection.
func wsReadLoop(ctx context.Context, conn *websocket.Conn, out chan<- model.Alert) {
	defer conn.Close() // Ensure connection is closed when this function returns

	// Ping/Pong Handling (slightly improved)
	// Server should ideally send pings; client responds with pongs.
	// If server doesn't ping, client can send pings to keep connection alive/detect failure.
	const pingInterval = 30 * time.Second
	const pongWait = pingInterval + (10 * time.Second) // Must be > pingInterval
	const writeWait = 10 * time.Second                 // Timeout for sending ping

	// Set read deadline slightly longer than ping interval
	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("Failed to set initial read deadline: %v", err)
		// Continue anyway, maybe it works without deadlines
	}
	conn.SetPongHandler(func(string) error {
		// log.Printf("Pong received") // Debugging line
		// Reset read deadline upon receiving a pong
		if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			log.Printf("Failed to set read deadline after pong: %v", err)
		}
		return nil
	})

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	readErrChan := make(chan error, 1) // Channel to signal read errors

	// Goroutine to read messages from the WebSocket
	go func() {
		defer close(readErrChan) // Close channel when read loop exits
		for {
			var alert model.Alert
			err := conn.ReadJSON(&alert) // Use ReadJSON for simplicity
			if err != nil {
				// Check if the error is expected (normal closure, going away)
				// or unexpected (indicates a problem).
				// IsCloseError is useful for specific close codes.
				// IsUnexpectedCloseError checks for common abrupt closures.
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket unexpected read error: %v", err)
				} else {
					// Could be normal closure, EOF, or json parsing error etc.
					log.Printf("WebSocket read loop exiting: %v", err)
				}
				readErrChan <- err // Signal the error (or nil if clean exit)
				return
			}
			// Successfully read an alert
			select {
			case out <- alert:
			case <-ctx.Done():
				log.Printf("Context cancelled while sending alert %s, discarding.", alert.ID)
				return // Exit read goroutine if context is cancelled
			default:
				// This case should ideally not happen if the main loop is processing fast enough
				// and the channel buffer is adequate. Consider increasing buffer if this occurs often.
				log.Printf("Warning: Alert channel buffer full. Discarding alert ID: %s", alert.ID)
			}
		}
	}()

	// Main loop for handling pings, context cancellation, and read errors
	for {
		select {
		case <-ctx.Done():
			log.Println("Context cancelled, closing WebSocket connection gracefully.")
			// Attempt to send a normal closure message
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Client shutting down"),
				time.Now().Add(writeWait))
			return // Exit wsReadLoop

		case <-pingTicker.C:
			// Send a ping message to the server
			deadline := time.Now().Add(writeWait)
			if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				log.Printf("Error sending WebSocket ping: %v", err)
				// Failure to send ping likely means connection is dead.
				return // Exit wsReadLoop, which will trigger reconnect in listenWS
			}
			// log.Printf("Ping sent") // Debugging line

		case err := <-readErrChan:
			// The read goroutine exited, either cleanly or with an error.
			if err != nil {
				log.Printf("Exiting WebSocket read loop due to error from read goroutine: %v", err)
			} else {
				log.Println("Exiting WebSocket read loop as read goroutine finished cleanly.")
			}
			return // Exit wsReadLoop
		}
	}
}

// pollHTTP periodically fetches alerts via HTTP GET requests.
func pollHTTP(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	// Increased timeout slightly for potentially slower API responses
	client := &http.Client{Timeout: 20 * time.Second}
	lastID := "" // Track the ID of the last processed alert

	// Use a Ticker for periodic polling
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	log.Printf("HTTP Polling configured with interval: %v", cfg.PollInterval)

	// Perform an initial fetch immediately before starting the loop
	log.Println("Performing initial HTTP poll...")
	if err := fetchAndProcessAlerts(ctx, client, cfg, &lastID, out); err != nil {
		// Log initial failure, but continue; ticker will retry.
		log.Printf("Error during initial HTTP poll: %v. Will retry on next interval.", err)
	}

	// Main polling loop
	for {
		select {
		case <-ctx.Done():
			log.Println("HTTP Polling stopping due to context cancellation.")
			return
		case <-ticker.C:
			log.Println("Polling for new alerts via HTTP...")
			if err := fetchAndProcessAlerts(ctx, client, cfg, &lastID, out); err != nil {
				// Log error but continue, the ticker will try again later
				log.Printf("Error during periodic HTTP poll: %v", err)
				// Consider implementing backoff here too if polls fail repeatedly
			}
		}
	}
}

// fetchAndProcessAlerts performs a single HTTP GET request for alerts.
func fetchAndProcessAlerts(ctx context.Context, client *http.Client, cfg *config.Config, lastID *string, out chan<- model.Alert) error {
	// Build URL *without* token in query params
	reqURL, err := buildHTTPPollURL(cfg.ServerURL, *lastID) // Pass only base URL and sinceID
	if err != nil {
		return fmt.Errorf("error building HTTP poll URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		// This error typically indicates a programming issue (invalid method/URL)
		return fmt.Errorf("internal error creating HTTP request: %w", err)
	}

	// --- Add Authorization Header ---
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "application/json") // Good practice

	// Log the request *without* the token for security
	log.Printf("Polling HTTP endpoint: %s (Authorization header used)", reqURL.Redacted())

	resp, err := client.Do(req)
	if err != nil {
		// Handle context cancellation explicitly
		if ctx.Err() != nil {
			return fmt.Errorf("http GET cancelled: %w", ctx.Err())
		}
		// Handle URL/network errors
		return fmt.Errorf("http GET error to %s: %w", reqURL.Redacted(), err)
	}
	defer resp.Body.Close() // Ensure body is always closed

	if resp.StatusCode != http.StatusOK {
		// Read limited amount of body for error reporting
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http GET %s returned status %s. Body sample: %s", reqURL.Redacted(), resp.Status, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body from %s: %w", reqURL.Redacted(), err)
	}

	// Handle empty response body explicitly
	if len(bodyBytes) == 0 || string(bodyBytes) == "[]" {
		log.Printf("No new alerts received via HTTP poll.")
		return nil
	}

	var alerts []model.Alert // Use model.Alert
	if err := json.Unmarshal(bodyBytes, &alerts); err != nil {
		// Provide more context on JSON unmarshal error
		logBody := string(bodyBytes)
		if len(logBody) > 512 { // Limit log size
			logBody = logBody[:512] + "..."
		}
		return fmt.Errorf("error unmarshalling JSON response from %s: %w. Body sample: %s", reqURL.Redacted(), err, logBody)
	}

	if len(alerts) == 0 {
		log.Printf("Received empty alert list via HTTP poll.")
		return nil
	}

	log.Printf("Received %d alert(s) via HTTP poll.", len(alerts))
	processedCount := 0
	maxID := *lastID // Keep track of the latest ID *in this batch*

	// Process alerts, updating lastID *after* successful dispatch
	for _, alert := range alerts {
		// Basic validation on received alert
		if alert.ID == "" {
			log.Printf("Warning: Received alert with empty ID via HTTP. Skipping.")
			continue
		}

		select {
		case out <- alert:
			// Update maxID seen in this batch. Assuming IDs are sortable/comparable.
			// If timestamps are more reliable for ordering, use that instead.
			// This simple logic assumes higher ID means newer.
			if alert.ID > maxID {
				maxID = alert.ID
			}
			processedCount++
		case <-ctx.Done():
			log.Printf("Context cancelled during HTTP alert processing. Aborting send for ID: %s", alert.ID)
			return ctx.Err() // Return context error to stop further processing
		default:
			log.Printf("Warning: Alert channel buffer full during HTTP poll. Discarding alert ID: %s", alert.ID)
			// Do not update lastID if alert is discarded
		}
	}

	// Update the persistent lastID only after processing the batch
	if processedCount > 0 && maxID > *lastID {
		*lastID = maxID
		log.Printf("Updated last processed alert ID to: %s", *lastID)
	}

	log.Printf("Processed %d/%d alerts from HTTP poll.", processedCount, len(alerts))
	return nil
}

// --- Helper Functions ---

// buildWebSocketURL now only takes baseURL and token
func buildWebSocketURL(baseURL, token string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL '%s': %w", baseURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported URL scheme '%s' for WebSocket", u.Scheme)
	}
	u.Path = "/ws/alerts" // Assuming fixed path, adjust if needed
	q := u.Query()
	q.Set("token", token) // Still setting token here - see security warning above
	u.RawQuery = q.Encode()
	return u, nil
}

// buildHTTPPollURL now only takes baseURL and sinceID
func buildHTTPPollURL(baseURL, sinceID string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL '%s': %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported URL scheme '%s' for HTTP", u.Scheme)
	}
	u.Path = "/api/alerts" // Assuming fixed path, adjust if needed
	q := u.Query()
	if sinceID != "" {
		q.Set("since", sinceID)
	}
	// Token is NO LONGER added to the query here
	u.RawQuery = q.Encode()
	return u, nil
}

// calculateBackoff computes the reconnection delay using exponential backoff with jitter.
func calculateBackoff(attempt int, maxInterval time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1 // Ensure attempt is at least 1
	}
	baseDelay := 500 * time.Millisecond
	maxDelay := float64(maxInterval)

	// Calculate exponential backoff: base * 2^(attempt-1)
	// Cap exponent to prevent overflow with very large attempt numbers
	const maxExponent = 20 // 2^20 is roughly 1 million, base * 1M should be large enough
	exponent := float64(attempt - 1)
	if exponent > maxExponent {
		exponent = maxExponent
	}
	delay := float64(baseDelay) * math.Pow(2, exponent)

	// Cap delay at maxInterval
	if delay > maxDelay || delay <= 0 { // Check for overflow resulting in <= 0
		delay = maxDelay
	}

	// Add jitter: random value between +/- 10% of the delay to prevent thundering herd
	// Use math/rand for slightly better randomness distribution if needed, but time is simpler
	// Ensure rand is seeded if using math/rand (e.g., rand.Seed(time.Now().UnixNano()))
	jitterFraction := (rand.Float64() * 0.2) - 0.1 // Range [-0.1, +0.1]
	jitter := delay * jitterFraction

	finalDelay := time.Duration(delay + jitter)

	// Clamp final delay again to be strictly within [baseDelay, maxInterval]
	if finalDelay < baseDelay {
		finalDelay = baseDelay
	}
	if finalDelay > maxInterval {
		finalDelay = maxInterval
	}

	return finalDelay
}
