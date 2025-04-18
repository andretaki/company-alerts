package main

import (
	"log"
	"net/http"
	"os"

	"github.com/yourorg/company-alerts/internal/model"
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/handlers"
	"github.com/yourorg/company-alerts/internal/server/hub"
)

func main() {
	// 1. Load configuration
	log.Println("Starting Company Alerts Server...")
	cfg, err := config.LoadServerConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// 2. Initialize WebSocket hub
	wsHub := hub.NewHub()
	go wsHub.Run()

	// 3. Initialize alert store and handlers
	alertStore := handlers.NewInMemoryAlertStore()
	alertsHandler := handlers.NewAlertsHandler(alertStore, wsHub, cfg)

	// 4. Add some test data if needed
	if os.Getenv("ADD_TEST_DATA") == "true" {
		addTestData(alertStore, wsHub)
	}

	// 5. Set up HTTP server with routing
	mux := http.NewServeMux()

	// Static file handling (for client UI if needed)
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("/", fs)

	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handlers.ServeWs(cfg, wsHub, w, r)
	})

	// Alert API endpoints
	alertsHandler.RegisterAlertRoutes(mux)

	// 6. Start server
	serverAddr := ":" + cfg.Port
	log.Printf("Server listening on %s", serverAddr)
	log.Printf("WebSocket endpoint: ws://localhost%s/ws", serverAddr)
	log.Printf("API endpoint: http://localhost%s/api/alerts", serverAddr)

	if err := http.ListenAndServe(serverAddr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// addTestData adds some sample alerts for testing
func addTestData(store *handlers.InMemoryAlertStore, hub *hub.Hub) {
	testAlerts := []model.Alert{
		{
			ID:        "test-1",
			Title:     "Test Info Alert",
			Message:   "This is a test info alert",
			Level:     "info",
			Timestamp: model.Time(),
		},
		{
			ID:        "test-2",
			Title:     "Test Warning Alert",
			Message:   "This is a test warning alert",
			Level:     "warning",
			Timestamp: model.Time(),
		},
		{
			ID:        "test-3",
			Title:     "Test Error Alert",
			Message:   "This is a test error alert",
			Level:     "error",
			Timestamp: model.Time(),
		},
	}

	for _, alert := range testAlerts {
		store.AddAlert(alert)
		log.Printf("Added test alert: %s", alert.String())
	}
}
