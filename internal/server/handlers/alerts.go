// internal/server/handlers/alerts.go
package handlers

import (
	"context"
	"database/sql" // Needed for sql.ErrNoRows
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings" // For path parsing and token splitting
	"sync"    // For mutex in InMemoryAlertStore
	"time"

	"github.com/google/uuid"
	"github.com/yourorg/company-alerts/internal/model"
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/hub"
	"github.com/yourorg/company-alerts/internal/storage" // Use storage package
)

// InMemoryAlertStore implements the same interface as storage.DB
// but keeps alerts in memory instead of persisting to a database.
type InMemoryAlertStore struct {
	mu     sync.RWMutex
	alerts map[string]model.Alert
}

// NewInMemoryAlertStore creates a new in-memory alert store.
func NewInMemoryAlertStore() *InMemoryAlertStore {
	return &InMemoryAlertStore{
		alerts: make(map[string]model.Alert),
	}
}

// SaveAlert stores an alert in memory.
func (s *InMemoryAlertStore) SaveAlert(ctx context.Context, a model.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure we have an ID
	if a.ID == "" {
		a.ID = uuid.NewString()
	}

	// Set timestamps if not already set
	now := time.Now().UTC()
	if a.Timestamp.IsZero() {
		a.Timestamp = now
	}

	// For new alerts, set CreatedAt
	if _, exists := s.alerts[a.ID]; !exists {
		a.CreatedAt = now
	}

	// Always update UpdatedAt on save
	a.UpdatedAt = now

	// Store in map
	s.alerts[a.ID] = a
	return nil
}

// GetAlertByID retrieves an alert by ID from memory.
func (s *InMemoryAlertStore) GetAlertByID(ctx context.Context, id string) (model.Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	alert, exists := s.alerts[id]
	if !exists {
		return model.Alert{}, sql.ErrNoRows
	}
	return alert, nil
}

// ListAlerts returns alerts filtered according to the provided criteria.
func (s *InMemoryAlertStore) ListAlerts(ctx context.Context, f storage.Filter) ([]model.Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Convert map to slice for filtering
	var allAlerts []model.Alert
	for _, a := range s.alerts {
		allAlerts = append(allAlerts, a)
	}

	// Apply filters
	var filtered []model.Alert
	for _, a := range allAlerts {
		if !matchesFilter(a, f) {
			continue
		}
		filtered = append(filtered, a)
	}

	// Sort by timestamp
	sortAlerts(filtered, f.SortAsc)

	// Apply pagination
	result := applyPagination(filtered, f.Offset, f.Limit)

	return result, nil
}

// Close is a no-op for in-memory store.
func (s *InMemoryAlertStore) Close() error {
	return nil
}

// Helper functions for InMemoryAlertStore

// matchesFilter checks if an alert matches the provided filter criteria.
func matchesFilter(a model.Alert, f storage.Filter) bool {
	// Type filter
	if len(f.Types) > 0 {
		typeMatch := false
		for _, t := range f.Types {
			if a.Type == t {
				typeMatch = true
				break
			}
		}
		if !typeMatch {
			return false
		}
	}

	// Severity filters
	if f.MinSeverity != nil && a.Severity < *f.MinSeverity {
		return false
	}
	if f.MaxSeverity != nil && a.Severity > *f.MaxSeverity {
		return false
	}

	// Status filter
	if f.Status != nil && a.Status != *f.Status {
		return false
	}

	// Source filter
	if f.Source != nil && a.Source != *f.Source {
		return false
	}

	// Reference ID filter
	if f.ReferenceID != nil && a.ReferenceID != *f.ReferenceID {
		return false
	}

	// Time range filters
	if f.Since != nil && a.Timestamp.Before(*f.Since) {
		return false
	}
	if f.Until != nil && a.Timestamp.After(*f.Until) {
		return false
	}

	return true
}

// sortAlerts sorts the alerts by timestamp.
func sortAlerts(alerts []model.Alert, ascending bool) {
	if ascending {
		sort.Slice(alerts, func(i, j int) bool {
			return alerts[i].Timestamp.Before(alerts[j].Timestamp)
		})
	} else {
		sort.Slice(alerts, func(i, j int) bool {
			return alerts[j].Timestamp.Before(alerts[i].Timestamp)
		})
	}
}

// applyPagination applies offset and limit to the alerts slice.
func applyPagination(alerts []model.Alert, offset, limit int) []model.Alert {
	// Apply defaults
	if limit <= 0 {
		limit = 100 // Default limit
	}
	if limit > 1000 {
		limit = 1000 // Max limit
	}
	if offset < 0 {
		offset = 0
	}

	// Check bounds
	if offset >= len(alerts) {
		return []model.Alert{}
	}

	// Calculate end index
	end := offset + limit
	if end > len(alerts) {
		end = len(alerts)
	}

	return alerts[offset:end]
}

// Define a named interface for alert storage
type AlertStore interface {
	SaveAlert(ctx context.Context, a model.Alert) error
	GetAlertByID(ctx context.Context, id string) (model.Alert, error)
	ListAlerts(ctx context.Context, f storage.Filter) ([]model.Alert, error)
	Close() error
}

// AlertsHandler manages HTTP requests for alerts, using DB storage.
type AlertsHandler struct {
	// Use interface type to support both storage.DB and InMemoryAlertStore
	store     AlertStore
	hub       *hub.Hub
	serverCfg *config.ServerConfig
}

// NewAlertsHandler creates a new alerts handler with the provided store.
// The store can be either a *storage.DB or an *InMemoryAlertStore.
func NewAlertsHandler(store interface{}, hub *hub.Hub, cfg *config.ServerConfig) *AlertsHandler {
	// Check if store is valid (either a *storage.DB or an *InMemoryAlertStore)
	var alertStore AlertStore
	switch s := store.(type) {
	case *storage.DB:
		if s == nil {
			log.Fatal("FATAL: AlertsHandler requires a non-nil database connection.")
		}
		alertStore = s
	case *InMemoryAlertStore:
		if s == nil {
			log.Fatal("FATAL: AlertsHandler requires a non-nil alert store.")
		}
		alertStore = s
	default:
		log.Fatal("FATAL: AlertsHandler requires a valid storage implementation (either *storage.DB or *InMemoryAlertStore).")
	}

	if hub == nil {
		log.Fatal("FATAL: AlertsHandler requires a non-nil WebSocket Hub.")
	}
	if cfg == nil {
		log.Fatal("FATAL: AlertsHandler requires a non-nil ServerConfig.")
	}

	return &AlertsHandler{
		store:     alertStore,
		hub:       hub,
		serverCfg: cfg,
	}
}

// authenticate checks the Authorization: Bearer <token> header.
func (h *AlertsHandler) authenticate(r *http.Request) (bool, string) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false, "" // No header
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		log.Printf("DEBUG: Invalid Authorization header format: %s", authHeader)
		return false, "" // Invalid format
	}
	token := parts[1]
	if h.serverCfg.IsTokenAllowed(token) {
		return true, token // Token is valid
	}
	log.Printf("WARN: Authentication failed for token prefix: %s...", token[:min(len(token), 4)])
	return false, "" // Token not allowed
}

// --- HTTP Handler Methods ---

// CreateAlert handles POST requests to create a new alert.
func (h *AlertsHandler) CreateAlert(w http.ResponseWriter, r *http.Request) {
	// 1. Method check is handled by the router (RegisterAlertRoutes)
	// 2. Authentication check is handled by the router or middleware (here manually)
	authenticated, _ := h.authenticate(r)
	if !authenticated {
		log.Println("WARN: Alert creation rejected: invalid or missing bearer token")
		http.Error(w, `{"error": "Unauthorized", "message": "Invalid or missing bearer token"}`, http.StatusUnauthorized)
		return
	}

	// 3. Parse request body into the unified model.Alert
	var alert model.Alert
	// Limit request body size to prevent potential abuse
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024) // 1MB limit

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields() // Prevent unexpected fields

	if err := decoder.Decode(&alert); err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		var maxBytesError *http.MaxBytesError

		switch {
		case errors.As(err, &syntaxError):
			msg := fmt.Sprintf("Request body contains badly-formed JSON (at character %d)", syntaxError.Offset)
			http.Error(w, `{"error": "Bad Request", "message": "`+msg+`"}`, http.StatusBadRequest)
		case errors.Is(err, io.ErrUnexpectedEOF):
			msg := "Request body contains badly-formed JSON"
			http.Error(w, `{"error": "Bad Request", "message": "`+msg+`"}`, http.StatusBadRequest)
		case errors.As(err, &unmarshalTypeError):
			msg := fmt.Sprintf("Request body contains an invalid value for the %q field (at character %d)", unmarshalTypeError.Field, unmarshalTypeError.Offset)
			http.Error(w, `{"error": "Bad Request", "message": "`+msg+`"}`, http.StatusBadRequest)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			msg := fmt.Sprintf("Request body contains unknown field %s", fieldName)
			http.Error(w, `{"error": "Bad Request", "message": "`+msg+`"}`, http.StatusBadRequest)
		case errors.Is(err, io.EOF):
			msg := "Request body must not be empty"
			http.Error(w, `{"error": "Bad Request", "message": "`+msg+`"}`, http.StatusBadRequest)
		case errors.As(err, &maxBytesError):
			msg := fmt.Sprintf("Request body must not be larger than %d bytes", maxBytesError.Limit)
			http.Error(w, `{"error": "Request Entity Too Large", "message": "`+msg+`"}`, http.StatusRequestEntityTooLarge)
		default:
			log.Printf("ERROR: Error decoding alert JSON: %v", err)
			http.Error(w, `{"error": "Internal Server Error", "message": "Failed to decode request body"}`, http.StatusInternalServerError)
		}
		return
	}

	// Assign server-generated fields or defaults if not provided by client
	if alert.ID == "" {
		alert.ID = uuid.NewString() // Generate ID if client didn't provide one
	}
	// Timestamp and Status validation/defaulting happens in Validate()

	// 4. Validate the alert data using the model's validation
	if err := alert.Validate(); err != nil {
		log.Printf("WARN: Invalid alert data received: %v (Alert: %+v)", err, alert)
		http.Error(w, `{"error": "Bad Request", "message": "Invalid alert data: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// 5. Store the alert in the database
	// Use request context for database operation cancellation.
	if err := h.store.SaveAlert(r.Context(), alert); err != nil {
		log.Printf("ERROR: Failed to save alert %s to database: %v", alert.ID, err)
		http.Error(w, `{"error": "Internal Server Error", "message": "Failed to save alert"}`, http.StatusInternalServerError)
		return
	}
	log.Printf("INFO: Alert created/updated via API: %s", alert.String())

	// 6. Broadcast the *saved* alert to WebSocket clients
	// It's good practice to fetch the alert again after saving if the DB modifies it (e.g., triggers)
	// For simplicity here, we broadcast the validated alert object.
	h.hub.Broadcast <- alert

	// 7. Return success response (echoing the created/updated alert)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated) // 201 Created is appropriate
	if err := json.NewEncoder(w).Encode(alert); err != nil {
		// Log error, but headers and status are likely already sent.
		log.Printf("ERROR: Error encoding success response for alert %s: %v", alert.ID, err)
	}
}

// ListAlerts handles GET requests to retrieve alerts with filtering.
func (h *AlertsHandler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	// 1. Method check handled by router
	// 2. Authentication
	authenticated, _ := h.authenticate(r)
	if !authenticated {
		log.Println("WARN: Alert listing rejected: invalid or missing bearer token")
		http.Error(w, `{"error": "Unauthorized", "message": "Invalid or missing bearer token"}`, http.StatusUnauthorized)
		return
	}

	// 3. Parse query parameters for filtering
	q := r.URL.Query()
	filter := storage.Filter{} // Use the storage package's Filter struct
	var parseErrors []string

	if typesStr := q.Get("type"); typesStr != "" {
		filter.Types = strings.Split(typesStr, ",")
		// Basic validation: trim spaces, remove empty strings
		validTypes := []string{}
		for _, t := range filter.Types {
			trimmed := strings.ToLower(strings.TrimSpace(t))
			if trimmed != "" {
				validTypes = append(validTypes, trimmed)
			}
		}
		filter.Types = validTypes
	}
	if minSevStr := q.Get("min_severity"); minSevStr != "" {
		if sev, err := strconv.Atoi(minSevStr); err == nil && sev >= 0 {
			filter.MinSeverity = &sev
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid min_severity parameter: '%s'", minSevStr))
		}
	}
	if maxSevStr := q.Get("max_severity"); maxSevStr != "" {
		if sev, err := strconv.Atoi(maxSevStr); err == nil && sev >= 0 {
			filter.MaxSeverity = &sev
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid max_severity parameter: '%s'", maxSevStr))
		}
	}
	if status := q.Get("status"); status != "" {
		trimmedStatus := strings.ToLower(strings.TrimSpace(status))
		filter.Status = &trimmedStatus // Store trimmed lowercase version
	}
	if source := q.Get("source"); source != "" {
		trimmedSource := strings.TrimSpace(source)
		filter.Source = &trimmedSource
	}
	if refID := q.Get("reference_id"); refID != "" {
		trimmedRefID := strings.TrimSpace(refID)
		filter.ReferenceID = &trimmedRefID
	}
	if sinceStr := q.Get("since"); sinceStr != "" {
		// Try parsing RFC3339Nano first, then RFC3339
		if t, err := time.Parse(time.RFC3339Nano, sinceStr); err == nil {
			tUTC := t.UTC()
			filter.Since = &tUTC
		} else if t, err2 := time.Parse(time.RFC3339, sinceStr); err2 == nil {
			tUTC := t.UTC()
			filter.Since = &tUTC
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid 'since' timestamp format (use RFC3339 or RFC3339Nano): '%s'", sinceStr))
		}
	}
	if untilStr := q.Get("until"); untilStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, untilStr); err == nil {
			tUTC := t.UTC()
			filter.Until = &tUTC
		} else if t, err2 := time.Parse(time.RFC3339, untilStr); err2 == nil {
			tUTC := t.UTC()
			filter.Until = &tUTC
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid 'until' timestamp format (use RFC3339 or RFC3339Nano): '%s'", untilStr))
		}
	}
	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit >= 0 {
			filter.Limit = limit // storage layer will apply default/max if needed
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid limit parameter: '%s'", limitStr))
		}
	}
	if offsetStr := q.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			filter.Offset = offset
		} else {
			parseErrors = append(parseErrors, fmt.Sprintf("Invalid offset parameter: '%s'", offsetStr))
		}
	}

	// Check for parsing errors
	if len(parseErrors) > 0 {
		errMsg := "Invalid query parameters: " + strings.Join(parseErrors, ", ")
		http.Error(w, `{"error": "Bad Request", "message": "`+errMsg+`"}`, http.StatusBadRequest)
		return
	}

	// 4. Get alerts from database using the filter
	alerts, err := h.store.ListAlerts(r.Context(), filter)
	if err != nil {
		log.Printf("ERROR: Failed to list alerts from database: %v", err)
		http.Error(w, `{"error": "Internal Server Error", "message": "Failed to retrieve alerts"}`, http.StatusInternalServerError)
		return
	}

	// 5. Return JSON response
	w.Header().Set("Content-Type", "application/json")
	// Ensure a non-nil JSON array `[]` is returned even if alerts list is empty.
	if alerts == nil {
		alerts = []model.Alert{}
	}
	if err := json.NewEncoder(w).Encode(alerts); err != nil {
		log.Printf("ERROR: Error encoding alerts list response: %v", err)
		// Can't reliably send error header now, just log.
	}
}

// GetAlert handles GET requests for a specific alert by ID (GET /api/alerts/{id}).
func (h *AlertsHandler) GetAlert(w http.ResponseWriter, r *http.Request, id string) {
	// 1. Method handled by router
	// 2. Authentication
	authenticated, _ := h.authenticate(r)
	if !authenticated {
		log.Println("WARN: Alert retrieval rejected: invalid or missing bearer token")
		http.Error(w, `{"error": "Unauthorized", "message": "Invalid or missing bearer token"}`, http.StatusUnauthorized)
		return
	}

	// 3. Validate ID (basic check, DB query handles actual existence)
	if id == "" {
		// This case should ideally be prevented by the router pattern
		http.Error(w, `{"error": "Bad Request", "message": "Alert ID is required in the path"}`, http.StatusBadRequest)
		return
	}

	// 4. Get alert from database
	alert, err := h.store.GetAlertByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") { // Check specific DB error
			http.Error(w, `{"error": "Not Found", "message": "Alert not found"}`, http.StatusNotFound)
		} else {
			log.Printf("ERROR: Failed to get alert %s from database: %v", id, err)
			http.Error(w, `{"error": "Internal Server Error", "message": "Failed to retrieve alert"}`, http.StatusInternalServerError)
		}
		return
	}

	// 5. Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(alert); err != nil {
		log.Printf("ERROR: Error encoding alert %s response: %v", id, err)
	}
}

// RegisterAlertRoutes sets up the alert-related HTTP endpoints using path-based routing.
func (h *AlertsHandler) RegisterAlertRoutes(mux *http.ServeMux) {
	// Handle requests to the base path /api/alerts/ (note trailing slash)
	alertBasePath := "/api/alerts/"
	mux.HandleFunc(alertBasePath, func(w http.ResponseWriter, r *http.Request) {
		// Extract potential ID from the path by trimming the base path prefix.
		// Example: /api/alerts/xyz-123 -> id = "xyz-123"
		// Example: /api/alerts/      -> id = ""
		id := strings.TrimPrefix(r.URL.Path, alertBasePath)

		// Check for invalid paths like /api/alerts/id/something/else
		if strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodPost:
			// POST is only allowed on the base collection path, not specific IDs
			if id != "" {
				http.Error(w, `{"error": "Method Not Allowed", "message": "POST method not allowed on specific alert ID"}`, http.StatusMethodNotAllowed)
				return
			}
			h.CreateAlert(w, r)
		case http.MethodGet:
			if id != "" {
				// Request is for a specific alert: GET /api/alerts/{id}
				h.GetAlert(w, r, id)
			} else {
				// Request is for listing alerts: GET /api/alerts/
				h.ListAlerts(w, r)
			}
		// --- Add other methods (PUT, PATCH, DELETE) here if needed ---
		// case http.MethodPut:
		//     if id == "" {
		//         http.Error(w, `{"error": "Method Not Allowed", "message": "PUT requires an alert ID in the path"}`, http.StatusMethodNotAllowed)
		//         return
		//     }
		//     h.UpdateAlert(w, r, id) // Implement UpdateAlert handler
		// case http.MethodDelete:
		//     if id == "" {
		//         http.Error(w, `{"error": "Method Not Allowed", "message": "DELETE requires an alert ID in the path"}`, http.StatusMethodNotAllowed)
		//         return
		//     }
		//     h.DeleteAlert(w, r, id) // Implement DeleteAlert handler
		default:
			// For unhandled methods on this path pattern
			http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
		}
	})

	// Optional: Redirect requests to /api/alerts (without trailing slash) to /api/alerts/
	// This provides a more consistent base URL.
	mux.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/alerts" { // Only redirect if path is exactly "/api/alerts"
			http.Redirect(w, r, alertBasePath, http.StatusPermanentRedirect)
		} else {
			// If path is something else starting with /api/alerts but not ending in /,
			// it might be caught by the handler above or treated as not found.
			// For robustness, explicitly treat other paths as not found.
			http.NotFound(w, r)
		}
	})
}

// Helper for logging token prefix
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
