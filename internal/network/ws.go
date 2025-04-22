// internal/network/ws.go
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv" // For query params
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yourorg/company-alerts/internal/config" // Client config
	"github.com/yourorg/company-alerts/internal/model"  // Unified model
)

// --- Remove the local Alert struct definition ---

// Global http client for polling and API calls
var httpClient = &http.Client{Timeout: 20 * time.Second}

// Listen starts the appropriate network listener (WebSocket or HTTP Polling)
func Listen(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	defer func() {
		// Ensure the output channel is closed when the listener exits,
		// regardless of the path (WS/HTTP or error).
		log.Println("Network listener goroutine finished.")
		close(out)
	}()

	mode := "HTTP Polling"
	if cfg.UseWebSocket {
		mode = "WebSocket"
		log.Printf("Network listener starting. Mode: %s", mode)
		listenWS(ctx, cfg, out) // Blocks until WS listener finishes
	} else {
		log.Printf("Network listener starting. Mode: %s", mode)
		pollHTTP(ctx, cfg, out) // Blocks until HTTP poller finishes
	}
}

// listenWS connects via WebSocket and listens for alerts. Handles reconnection.
// It now blocks until the context is cancelled or an unrecoverable error occurs.
func listenWS(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	wsURL, err := buildWebSocketURL(cfg.ServerURL, cfg.Token)
	if err != nil {
		log.Printf("FATAL: Error building WebSocket URL: %v. Cannot start listener.", err)
		return // Exit the goroutine
	}
	log.Printf("Attempting to connect to WebSocket: %s", wsURL.Redacted()) // Redacted hides query params

	reconnectAttempts := 0
	maxReconnectInterval := 1 * time.Minute
	rand.Seed(time.Now().UnixNano()) // Seed random for jitter

	for {
		// --- Connection Loop ---
		select {
		case <-ctx.Done():
			log.Println("WebSocket listener stopping before connect attempt due to context cancellation.")
			return // Exit connection loop
		default:
			// Proceed with connection attempt
		}

		dialer := websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 15 * time.Second,
		}
		// NOTE: Still using token in query parameter here.
		// Consider header auth if server/client support allows:
		// header := http.Header{}
		// header.Add("Authorization", "Bearer "+cfg.Token)
		// conn, resp, err := dialer.DialContext(ctx, wsURL.String(), header)
		conn, resp, err := dialer.DialContext(ctx, wsURL.String(), nil)

		if err != nil {
			// Check if context was cancelled during dial
			if ctx.Err() != nil {
				log.Printf("WebSocket dial cancelled by context: %v", ctx.Err())
				return // Exit connection loop
			}

			// Handle connection error and schedule reconnect
			reconnectAttempts++
			delay := calculateBackoff(reconnectAttempts, maxReconnectInterval)
			status := "N/A"
			errMsg := err.Error()
			if resp != nil {
				status = resp.Status
				// Attempt to read body for more info, but handle potential errors
				bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
				resp.Body.Close() // Close body immediately
				if readErr == nil {
					errMsg = fmt.Sprintf("%v (status: %s, body: %s)", err, status, string(bodyBytes))
				} else {
					errMsg = fmt.Sprintf("%v (status: %s, body read error: %v)", err, status, readErr)
				}
			} else {
				errMsg = fmt.Sprintf("%v (no HTTP response)", err)
			}

			log.Printf("WebSocket dial error: %s. Reconnecting attempt %d in %v...", errMsg, reconnectAttempts, delay)

			// Wait for backoff duration or context cancellation
			select {
			case <-time.After(delay):
				continue // Retry connection
			case <-ctx.Done():
				log.Println("WebSocket listener stopping during reconnect backoff due to context cancellation.")
				return // Exit connection loop
			}
		}

		// Close response body defensively if not already closed
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		log.Println("WebSocket connection established.")
		reconnectAttempts = 0 // Reset attempts on successful connection

		// --- Read Loop ---
		// wsReadLoop now runs *within* the connection loop.
		// It returns when the connection is closed or context is cancelled.
		wsReadLoop(ctx, conn, out) // This blocks until the connection breaks/closes

		// --- Post Read Loop ---
		log.Println("WebSocket connection closed or read loop exited.")

		// Check context *before* deciding to reconnect. If context is done, exit.
		select {
		case <-ctx.Done():
			log.Println("WebSocket listener stopping after connection close due to context cancellation.")
			return // Exit connection loop
		default:
			// Context not cancelled, add a small delay before reconnect attempt
			// This prevents instant hammering if the server immediately closes the connection.
			time.Sleep(1*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond)
			log.Println("Attempting WebSocket reconnect...")
			// Loop continues to retry connection
		}
	} // End of connection loop
}

// wsReadLoop continuously reads messages (expecting model.Alert JSON).
// Returns when the connection breaks or context is cancelled.
func wsReadLoop(ctx context.Context, conn *websocket.Conn, out chan<- model.Alert) {
	defer func() {
		log.Printf("Closing WebSocket connection from read loop for %s", conn.RemoteAddr())
		conn.Close()
	}()

	const pingInterval = 30 * time.Second
	const pongWait = pingInterval + (15 * time.Second) // Increased pongWait slightly
	const writeWait = 10 * time.Second

	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("Warning: Failed to set initial read deadline: %v", err)
	}
	conn.SetPongHandler(func(string) error {
		// log.Printf("Pong received from %s", conn.RemoteAddr()) // Debug
		if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			log.Printf("Warning: Failed to set read deadline after pong: %v", err)
		}
		return nil
	})

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	readErrChan := make(chan error, 1) // Channel to signal read errors/completion

	// Goroutine to read messages from the WebSocket connection
	go func() {
		defer close(readErrChan) // Signal completion when this goroutine exits
		for {
			// Expecting JSON messages matching model.Alert
			var alert model.Alert
			err := conn.ReadJSON(&alert) // Read unified model.Alert
			if err != nil {
				// Log appropriate message based on error type
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
					log.Printf("WebSocket unexpected read error for %s: %v", conn.RemoteAddr(), err)
				} else if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "closed") {
					log.Printf("WebSocket connection closed by peer (%s): %v", conn.RemoteAddr(), err)
				} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("WebSocket read timeout (%s): %v", conn.RemoteAddr(), err)
				} else {
					// Includes JSON decoding errors, etc.
					log.Printf("WebSocket read error (%s): %v", conn.RemoteAddr(), err)
				}
				readErrChan <- err // Signal the error that caused the loop to exit
				return             // Exit the read goroutine
			}

			// Successfully read an alert
			select {
			case out <- alert:
				// log.Printf("Received alert via WS: %s", alert.ID) // Debug
			case <-ctx.Done():
				log.Printf("Context cancelled while queuing WS alert %s, discarding.", alert.ID)
				readErrChan <- ctx.Err() // Signal context cancellation
				return                   // Exit the read goroutine
			default:
				// This case should ideally not happen if the main loop processes fast enough
				// and the channel buffer is adequate.
				log.Printf("Warning: Alert channel buffer full (WS). Discarding alert ID: %s", alert.ID)
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
			// Wait briefly for read goroutine to potentially finish upon context cancellation signal
			time.Sleep(100 * time.Millisecond)
			return // Exit wsReadLoop

		case <-pingTicker.C:
			// Send a ping message to the server
			deadline := time.Now().Add(writeWait)
			if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				log.Printf("Error sending WebSocket ping to %s: %v. Assuming connection lost.", conn.RemoteAddr(), err)
				// Don't need to explicitly close conn here, defer handles it.
				return // Exit wsReadLoop, which will trigger reconnect in listenWS outer loop
			}
			// log.Printf("Ping sent to %s", conn.RemoteAddr()) // Debug

		case readErr := <-readErrChan:
			// The read goroutine exited, either cleanly (nil) or with an error.
			if readErr != nil && !errors.Is(readErr, context.Canceled) { // Don't log cancellation as an error here
				log.Printf("Exiting WebSocket read loop due to error from read goroutine: %v", readErr)
			} else if readErr == nil {
				log.Println("Exiting WebSocket read loop as read goroutine finished cleanly (e.g., peer closed connection).")
			} else {
				log.Println("Exiting WebSocket read loop due to context cancellation signal from read goroutine.")
			}
			return // Exit wsReadLoop
		}
	}
}

// pollHTTP periodically fetches alerts via HTTP GET requests.
// Blocks until context is cancelled.
func pollHTTP(ctx context.Context, cfg *config.Config, out chan<- model.Alert) {
	// lastTimestamp tracks the timestamp of the *newest* alert received in the previous poll.
	// We poll for alerts *after* this time. Initialize to zero time for the first poll.
	var lastTimestamp time.Time
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	log.Printf("HTTP Polling configured with interval: %v", cfg.PollInterval)

	// Perform initial fetch immediately
	log.Println("Performing initial HTTP poll...")
	newTimestamp, err := fetchAndProcessAlerts(ctx, cfg, lastTimestamp, out)
	if err != nil {
		// Log initial error but continue; ticker will retry. Don't update timestamp.
		log.Printf("Error during initial HTTP poll: %v. Will retry on next interval.", err)
	} else if !newTimestamp.IsZero() {
		lastTimestamp = newTimestamp
	}

	// Main polling loop
	for {
		select {
		case <-ctx.Done():
			log.Println("HTTP Polling stopping due to context cancellation.")
			return // Exit loop and goroutine
		case <-ticker.C:
			// log.Printf("Polling for new alerts via HTTP since %s...", lastTimestamp.Format(time.RFC3339)) // Reduce noise
			newTimestamp, err := fetchAndProcessAlerts(ctx, cfg, lastTimestamp, out)
			if err != nil {
				// Log error but continue, ticker will try again later. Don't update timestamp on error.
				log.Printf("Error during periodic HTTP poll: %v", err)
				// Consider implementing backoff here too if polls fail repeatedly
			} else if !newTimestamp.IsZero() {
				// Update timestamp only if the poll was successful and returned alerts
				lastTimestamp = newTimestamp
			}
		}
	}
}

// fetchAndProcessAlerts performs a single HTTP GET request for alerts created *after* the 'since' time.
// Returns the timestamp of the latest alert processed, or zero time if none/error.
func fetchAndProcessAlerts(ctx context.Context, cfg *config.Config, since time.Time, out chan<- model.Alert) (time.Time, error) {
	// Use a timeout for the HTTP request itself, derived from the main context
	reqCtx, cancel := context.WithTimeout(ctx, httpClient.Timeout) // Use client timeout duration
	defer cancel()

	reqURL, err := buildHTTPPollURL(cfg.ServerURL, since) // Use 'since' timestamp
	if err != nil {
		return time.Time{}, fmt.Errorf("error building HTTP poll URL: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		// Should not happen with valid inputs
		return time.Time{}, fmt.Errorf("internal error creating HTTP request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.Token) // Use Bearer token
	req.Header.Set("Accept", "application/json")

	// log.Printf("Polling HTTP endpoint: %s", reqURL.Redacted()) // Reduce log noise

	resp, err := httpClient.Do(req)
	if err != nil {
		// Check if the error is due to context cancellation (either main ctx or reqCtx timeout)
		if errors.Is(err, context.DeadlineExceeded) {
			return time.Time{}, fmt.Errorf("http GET %s timeout: %w", reqURL.Redacted(), err)
		}
		if errors.Is(err, context.Canceled) {
			return time.Time{}, fmt.Errorf("http GET %s cancelled: %w", reqURL.Redacted(), err)
		}
		// Other potential errors (connection refused, DNS lookup failure, etc.)
		return time.Time{}, fmt.Errorf("http GET error to %s: %w", reqURL.Redacted(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read limited body for error
		return time.Time{}, fmt.Errorf("http GET %s returned status %s. Body sample: %s", reqURL.Redacted(), resp.Status, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, fmt.Errorf("error reading response body from %s: %w", reqURL.Redacted(), err)
	}

	if len(bodyBytes) == 0 || string(bodyBytes) == "[]" {
		// log.Printf("No new alerts received via HTTP poll.") // Reduce log noise
		return time.Time{}, nil // No new alerts, return zero time (don't update lastTimestamp)
	}

	var alerts []model.Alert // Use unified model.Alert
	if err := json.Unmarshal(bodyBytes, &alerts); err != nil {
		logBody := string(bodyBytes)
		if len(logBody) > 512 { // Limit log size for readability
			logBody = logBody[:512] + "..."
		}
		return time.Time{}, fmt.Errorf("error unmarshalling JSON response from %s: %w. Body sample: %s", reqURL.Redacted(), err, logBody)
	}

	if len(alerts) == 0 {
		// log.Printf("Received empty alert list via HTTP poll.") // Reduce log noise
		return time.Time{}, nil // No new alerts
	}

	log.Printf("Received %d alert(s) via HTTP poll.", len(alerts))
	processedCount := 0
	latestTimestampInBatch := since // Initialize with the 'since' time

	// Process alerts. Server should return them ordered oldest to newest (timestamp ASC)
	// when using a 'since' parameter. We update latestTimestampInBatch as we go.
	for _, alert := range alerts {
		alert.Timestamp = alert.Timestamp.UTC() // Ensure UTC

		// Keep track of the timestamp of the newest alert in this batch
		if alert.Timestamp.After(latestTimestampInBatch) {
			latestTimestampInBatch = alert.Timestamp
		}

		// Send alert to the main application loop
		select {
		case out <- alert:
			processedCount++
		case <-ctx.Done():
			// Main context cancelled while processing, stop sending.
			log.Printf("Context cancelled during HTTP alert processing. Aborting send for ID: %s", alert.ID)
			// Return the latest timestamp seen *before* cancellation, so the next poll doesn't miss alerts.
			return latestTimestampInBatch, ctx.Err()
		default:
			// This indicates the main loop's channel buffer is full.
			// This is problematic as we might lose alerts.
			// For now, log and discard, but consider strategies like blocking or resizing buffer.
			log.Printf("Warning: Alert channel buffer full (HTTP). Discarding alert ID: %s", alert.ID)
			// Do not increment processedCount if discarded.
		}
	}

	log.Printf("Processed %d/%d alerts from HTTP poll. Newest timestamp in batch: %s",
		processedCount, len(alerts), latestTimestampInBatch.Format(time.RFC3339))

	// Return the timestamp of the newest alert successfully processed in this batch.
	// The outer loop will use this for the 'since' parameter of the *next* poll.
	return latestTimestampInBatch, nil
}

// FetchHistory retrieves recent alerts via the API.
func FetchHistory(ctx context.Context, cfg *config.Config, limit int) ([]model.Alert, error) {
	apiURL, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL '%s': %w", cfg.ServerURL, err)
	}
	// Ensure the path points to the base alert endpoint
	apiURL.Path = strings.TrimSuffix(apiURL.Path, "/") + "/api/alerts/"

	q := apiURL.Query()
	q.Set("limit", strconv.Itoa(limit))
	// Assuming server sorts by timestamp desc by default or supports a sort parameter
	// q.Set("sort", "timestamp_desc")
	apiURL.RawQuery = q.Encode()

	// Use a timeout specific to this API call
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second) // Timeout for history fetch
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("internal error creating history request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Accept", "application/json")

	log.Printf("Fetching history from API: %s", apiURL.Redacted())

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("history API GET timeout: %w", err)
		}
		return nil, fmt.Errorf("history API GET error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) // Read limited body for error info
		return nil, fmt.Errorf("history API GET returned status %s. Body sample: %s", resp.Status, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading history response body: %w", err)
	}

	var alerts []model.Alert
	if err := json.Unmarshal(bodyBytes, &alerts); err != nil {
		return nil, fmt.Errorf("error unmarshalling history response JSON: %w", err)
	}

	log.Printf("Fetched %d alerts for history view.", len(alerts))
	// Ensure timestamps are UTC after fetching
	for i := range alerts {
		alerts[i].Timestamp = alerts[i].Timestamp.UTC()
	}
	return alerts, nil
}

// --- Helper Functions ---

// buildWebSocketURL constructs the WebSocket URL (ws:// or wss://).
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
		return nil, fmt.Errorf("unsupported scheme '%s' for WebSocket", u.Scheme)
	}
	// Ensure path is correct for WebSocket endpoint, e.g., "/ws"
	// This depends on how the server routes WebSocket connections.
	u.Path = "/ws" // Assuming server handles '/ws' for websockets. Adjust if needed.

	q := u.Query()
	q.Set("token", token) // Still using query param token for WS auth
	u.RawQuery = q.Encode()
	return u, nil
}

// buildHTTPPollURL constructs the URL for polling alerts (http:// or https://).
// It includes a 'since' timestamp query parameter.
func buildHTTPPollURL(baseURL string, since time.Time) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL '%s': %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme '%s' for HTTP", u.Scheme)
	}
	// Ensure path points to the base API alert endpoint
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/alerts/"

	q := u.Query()
	if !since.IsZero() {
		// Use RFC3339 format (includes Z for UTC), which is commonly parseable.
		// Server needs to handle this format. Add fractional seconds for more precision.
		q.Set("since", since.UTC().Format(time.RFC3339Nano))
	}
	// Limit the number of alerts returned per poll to avoid overwhelming the client.
	// The server should also enforce a max limit.
	q.Set("limit", "100")
	// Request alerts sorted by timestamp ascending when using 'since',
	// so we process them in order and correctly update our latest timestamp.
	q.Set("sort", "timestamp_asc") // Assuming server supports this

	u.RawQuery = q.Encode()
	return u, nil
}

// calculateBackoff computes exponential backoff with jitter.
func calculateBackoff(attempt int, maxInterval time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	baseDelay := 500 * time.Millisecond
	maxDelay := float64(maxInterval)
	const maxExponent = 20 // Prevents excessive delays (2^20 * 500ms is ~145 hrs)

	// Calculate exponential backoff: base * 2^(attempt-1)
	exponent := math.Min(float64(attempt-1), maxExponent)
	delay := math.Min(float64(baseDelay)*math.Pow(2, exponent), maxDelay)

	// Add jitter: +/- 20% of the calculated delay
	jitterFraction := (rand.Float64() * 0.4) - 0.2 // Range [-0.2, +0.2]
	jitter := delay * jitterFraction
	finalDelay := time.Duration(delay + jitter)

	// Clamp final delay to be strictly within [baseDelay, maxInterval]
	if finalDelay < baseDelay {
		finalDelay = baseDelay
	}
	if finalDelay > maxInterval {
		finalDelay = maxInterval
	}
	return finalDelay
}
