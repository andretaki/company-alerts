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
	"net/http"
	"net/url"

	// "strconv" // No longer needed after Alert.String() change
	"strings"
	"time"

	// These should be resolved by `go mod tidy`
	"github.com/gorilla/websocket"
	"github.com/yourorg/company-alerts/internal/config" // Use concrete config type
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
func Listen(ctx context.Context, cfg *config.Config, out chan<- Alert) {
	log.Printf("Network listener starting. Mode: %s", map[bool]string{true: "WebSocket", false: "HTTP Polling"}[cfg.UseWebSocket])
	if cfg.UseWebSocket {
		go listenWS(ctx, cfg, out)
	} else {
		go pollHTTP(ctx, cfg, out)
	}
}

// listenWS connects via WebSocket and listens for alerts. Handles reconnection.
func listenWS(ctx context.Context, cfg *config.Config, out chan<- Alert) {
	wsURL, err := buildWebSocketURL(cfg.ServerURL, cfg.Token)
	if err != nil {
		log.Printf("Error building WebSocket URL: %v. WebSocket listener will not start.", err)
		return
	}
	log.Printf("Connecting to WebSocket: %s", wsURL.Redacted())

	reconnectAttempts := 0
	maxReconnectInterval := 1 * time.Minute

	for {
		select {
		case <-ctx.Done():
			log.Println("WebSocket listener stopping due to context cancellation.")
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.Dial(wsURL.String(), nil)
		if err != nil {
			reconnectAttempts++
			delay := calculateBackoff(reconnectAttempts, maxReconnectInterval)
			log.Printf("WebSocket dial error: %v. Reconnecting attempt %d in %v...", err, reconnectAttempts, delay)
			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				log.Println("WebSocket listener stopping during reconnect backoff due to context cancellation.")
				return
			}
		}

		log.Println("WebSocket connection established.")
		reconnectAttempts = 0
		wsReadLoop(ctx, conn, out)
		log.Println("WebSocket connection closed. Attempting to reconnect...")
		select {
		case <-time.After(1 * time.Second): // Small delay before immediate reconnect attempt
		case <-ctx.Done():
			log.Println("WebSocket listener stopping during reconnect delay due to context cancellation.")
			return
		}
	}
}

// wsReadLoop continuously reads messages from the WebSocket connection.
func wsReadLoop(ctx context.Context, conn *websocket.Conn, out chan<- Alert) {
	defer conn.Close()

	pingInterval := 30 * time.Second
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	readErrChan := make(chan error, 1)

	go func() {
		for {
			var alert Alert
			if err := conn.ReadJSON(&alert); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
					log.Printf("WebSocket read error: %v", err)
				} else {
					log.Printf("WebSocket connection closed gracefully or as expected.")
				}
				readErrChan <- err
				return
			}
			select {
			case out <- alert:
			case <-ctx.Done(): // Prevent blocking on send if context is cancelled
				return
			default:
				log.Printf("Warning: Alert channel full. Discarding alert ID: %s", alert.ID)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			log.Println("Context cancelled, closing WebSocket connection.")
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Client shutting down"))
			return
		case <-ticker.C:
			deadline := time.Now().Add(10 * time.Second)
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, deadline); err != nil {
				log.Printf("Error sending WebSocket ping: %v", err)
				return // Exit read loop, outer loop will reconnect
			}
		case err := <-readErrChan:
			if err != nil && websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
				log.Printf("Exiting WebSocket read loop due to read error: %v", err)
			} else {
				log.Println("Exiting WebSocket read loop (connection closed).")
			}
			return
		}
	}
}

// pollHTTP periodically fetches alerts via HTTP GET requests.
func pollHTTP(ctx context.Context, cfg *config.Config, out chan<- Alert) {
	client := &http.Client{Timeout: 15 * time.Second}
	lastID := ""
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	log.Printf("HTTP Polling configured with interval: %v", cfg.PollInterval)

	// Initial fetch before starting the ticker loop
	if err := fetchAndProcessAlerts(ctx, client, cfg, &lastID, out); err != nil {
		log.Printf("Error during initial HTTP poll: %v", err)
		// Consider initial poll failure strategy - retry or wait for first tick?
		// Waiting for first tick is simpler for now.
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("HTTP Polling stopping due to context cancellation.")
			return
		case <-ticker.C:
			if err := fetchAndProcessAlerts(ctx, client, cfg, &lastID, out); err != nil {
				log.Printf("Error during HTTP poll: %v", err)
			}
		}
	}
}

// fetchAndProcessAlerts performs a single HTTP GET request for alerts.
func fetchAndProcessAlerts(ctx context.Context, client *http.Client, cfg *config.Config, lastID *string, out chan<- Alert) error {
	reqURL, err := buildHTTPPollURL(cfg.ServerURL, cfg.Token, *lastID)
	if err != nil {
		return fmt.Errorf("error building HTTP poll URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("error creating HTTP request: %w", err)
	}

	log.Printf("Polling HTTP endpoint: %s", reqURL.Redacted())
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http GET error to %s: %w", reqURL.Redacted(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("http GET %s returned status %s. Body: %s", reqURL.Redacted(), resp.Status, string(bodyBytes))
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body from %s: %w", reqURL.Redacted(), err)
	}

	var alerts []Alert
	if err := json.Unmarshal(bodyBytes, &alerts); err != nil {
		if strings.TrimSpace(string(bodyBytes)) == "[]" || strings.TrimSpace(string(bodyBytes)) == "" {
			log.Printf("No new alerts received via HTTP poll.")
			return nil
		}
		return fmt.Errorf("error unmarshalling JSON response from %s: %w. Body: %s", reqURL.Redacted(), err, string(bodyBytes))
	}

	log.Printf("Received %d alert(s) via HTTP poll.", len(alerts))
	processedCount := 0
	for _, alert := range alerts {
		select {
		case out <- alert:
			if alert.ID != "" {
				*lastID = alert.ID // Update lastID only for successfully sent alerts with ID
			}
			processedCount++
		case <-ctx.Done(): // Don't block sending if shutting down
			log.Printf("Context cancelled during HTTP alert processing. Aborting send for ID: %s", alert.ID)
			return ctx.Err() // Return context error
		default:
			log.Printf("Warning: Alert channel full during HTTP poll. Discarding alert ID: %s", alert.ID)
		}
	}
	log.Printf("Processed %d/%d alerts from HTTP poll.", processedCount, len(alerts))
	return nil
}

// --- Helper Functions ---

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
		return nil, fmt.Errorf("unsupported scheme '%s'", u.Scheme)
	}
	u.Path = "/ws/alerts" // Assuming fixed path
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u, nil
}

func buildHTTPPollURL(baseURL, token, sinceID string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL '%s': %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme '%s'", u.Scheme)
	}
	u.Path = "/api/alerts" // Assuming fixed path
	q := u.Query()
	q.Set("token", token)
	if sinceID != "" {
		q.Set("since", sinceID)
	}
	u.RawQuery = q.Encode()
	return u, nil
}

// calculateBackoff computes the reconnection delay using exponential backoff with jitter.
func calculateBackoff(attempt int, maxInterval time.Duration) time.Duration {
	baseDelay := 500 * time.Millisecond
	maxDelay := float64(maxInterval)
	// Calculate exponential backoff: base * 2^(attempt-1)
	delay := float64(baseDelay) * math.Pow(2, float64(attempt-1))

	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter: random value between -10% and +10% of the delay
	jitterFraction := (float64(time.Now().UnixNano()%1000) / 1000.0) * 0.2 // Random fraction 0 to 0.2
	jitter := delay * (jitterFraction - 0.1)                               // Shift range to -0.1 to +0.1

	finalDelay := time.Duration(delay + jitter)

	// Clamp final delay between baseDelay and maxInterval
	if finalDelay < baseDelay {
		finalDelay = baseDelay
	}
	if finalDelay > maxInterval {
		finalDelay = maxInterval
	}
	return finalDelay
}
