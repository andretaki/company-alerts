package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/yourorg/company-alerts/internal/model"
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/hub"
)

// InMemoryAlertStore provides a simple in-memory storage for alerts
type InMemoryAlertStore struct {
	alerts []model.Alert
	mu     sync.RWMutex
}

// NewInMemoryAlertStore creates a new in-memory alert store
func NewInMemoryAlertStore() *InMemoryAlertStore {
	return &InMemoryAlertStore{
		alerts: make([]model.Alert, 0),
	}
}

// AddAlert adds a new alert to the store
func (s *InMemoryAlertStore) AddAlert(alert model.Alert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, alert)
}

// GetAlerts returns all alerts in the store
func (s *InMemoryAlertStore) GetAlerts() []model.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent race conditions
	result := make([]model.Alert, len(s.alerts))
	copy(result, s.alerts)
	return result
}

// AlertsHandler manages HTTP requests for alerts
type AlertsHandler struct {
	store     *InMemoryAlertStore
	hub       *hub.Hub
	serverCfg *config.ServerConfig
}

// NewAlertsHandler creates a new alerts handler
func NewAlertsHandler(store *InMemoryAlertStore, hub *hub.Hub, cfg *config.ServerConfig) *AlertsHandler {
	return &AlertsHandler{
		store:     store,
		hub:       hub,
		serverCfg: cfg,
	}
}

// CreateAlert handles POST requests to create a new alert
func (h *AlertsHandler) CreateAlert(w http.ResponseWriter, r *http.Request) {
	// 1. Verify method
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. Authenticate request
	token := r.Header.Get("X-API-Token")
	if token == "" {
		log.Println("Alert creation rejected: missing token")
		http.Error(w, "Missing token", http.StatusUnauthorized)
		return
	}
	if !h.serverCfg.IsTokenAllowed(token) {
		log.Printf("Alert creation rejected: invalid token provided")
		http.Error(w, "Invalid token", http.StatusForbidden)
		return
	}

	// 3. Parse request body
	var alertRequest struct {
		Title   string `json:"title"`
		Message string `json:"message"`
		Level   string `json:"level"` // info, warning, error, critical
	}

	if err := json.NewDecoder(r.Body).Decode(&alertRequest); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if alertRequest.Title == "" || alertRequest.Message == "" {
		http.Error(w, "Title and message are required", http.StatusBadRequest)
		return
	}

	// Validate alert level
	if alertRequest.Level == "" {
		alertRequest.Level = "info" // Default level
	} else if alertRequest.Level != "info" &&
		alertRequest.Level != "warning" &&
		alertRequest.Level != "error" &&
		alertRequest.Level != "critical" {
		http.Error(w, "Invalid alert level. Must be: info, warning, error, or critical", http.StatusBadRequest)
		return
	}

	// 4. Create the alert
	now := time.Now()
	alert := model.Alert{
		ID:        uuid.New().String(),
		Title:     alertRequest.Title,
		Message:   alertRequest.Message,
		Level:     alertRequest.Level,
		Timestamp: now,
	}

	// 5. Store the alert
	h.store.AddAlert(alert)
	log.Printf("Alert created: %s - %s", alert.ID, alert.Title)

	// 6. Broadcast to WebSocket clients
	h.hub.Broadcast <- alert

	// 7. Return success response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	response := struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Message   string    `json:"message"`
		Level     string    `json:"level"`
		Timestamp time.Time `json:"timestamp"`
	}{
		ID:        alert.ID,
		Title:     alert.Title,
		Message:   alert.Message,
		Level:     alert.Level,
		Timestamp: alert.Timestamp,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("Error encoding response: %v", err)
	}
}

// ListAlerts handles GET requests to retrieve alerts
func (h *AlertsHandler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	// 1. Verify method
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 2. Authenticate request
	token := r.Header.Get("X-API-Token")
	if token == "" {
		log.Println("Alert listing rejected: missing token")
		http.Error(w, "Missing token", http.StatusUnauthorized)
		return
	}
	if !h.serverCfg.IsTokenAllowed(token) {
		log.Printf("Alert listing rejected: invalid token provided")
		http.Error(w, "Invalid token", http.StatusForbidden)
		return
	}

	// 3. Get alerts from store
	alerts := h.store.GetAlerts()

	// 4. Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(alerts); err != nil {
		log.Printf("Error encoding alerts: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// RegisterAlertRoutes sets up the alert-related HTTP endpoints
func (h *AlertsHandler) RegisterAlertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			h.CreateAlert(w, r)
		case http.MethodGet:
			h.ListAlerts(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}
