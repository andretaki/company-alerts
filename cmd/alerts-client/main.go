// cmd/alerts-server/main.go
package main

import (
	"context" // Needed for sql.ErrNoRows check in healthz
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid" // For test data ID
	// Import server packages
	"github.com/yourorg/company-alerts/internal/model" // For test data
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/handlers"
	"github.com/yourorg/company-alerts/internal/server/hub"
	"github.com/yourorg/company-alerts/internal/storage" // Import storage
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Company Alerts Server...")

	// 1. Load Server Configuration
	cfg, err := config.LoadServerConfig()
	if err != nil {
		log.Fatalf("FATAL: Failed to load server configuration: %v", err)
	}
	log.Printf("Server configuration loaded. Port: %s, Allowed Tokens Loaded: %d", cfg.Port, len(cfg.AllowedTokens))
	if len(cfg.AllowedTokens) == 0 {
		log.Println("WARNING: Server starting with NO allowed tokens. No clients or API calls can authenticate.")
	}

	// 2. Initialize Database Connection
	db, err := storage.Open() // Use the storage package
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		} else {
			log.Println("Database connection closed.")
		}
	}()
	log.Println("Database connection established and schema verified.")

	// 3. Create and Run WebSocket Hub
	websocketHub := hub.NewHub()
	go websocketHub.Run() // Run the hub's event loop in the background

	// 4. Initialize handlers using the DB connection
	alertsHandler := handlers.NewAlertsHandler(db, websocketHub, cfg) // Pass db

	// 5. Add some test data if needed (Now uses the DB)
	if os.Getenv("ADD_TEST_DATA") == "true" {
		addTestData(db, websocketHub) // Pass DB and Hub
	}

	// 6. Setup HTTP Server and Routes
	mux := http.NewServeMux()

	// WebSocket endpoint - Requires token in query param: ws://.../ws?token=...
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handlers.ServeWs(cfg, websocketHub, w, r) // Pass config and hub
	})

	// Alert API endpoints (Now handles /api/alerts/ and /api/alerts/{id})
	alertsHandler.RegisterAlertRoutes(mux)

	// Simple health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Check DB connection
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			log.Printf("Health check failed: DB ping error: %v", err)
			http.Error(w, "Not OK - DB Error", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Static file handling (for client UI if needed)
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static" // Default static dir
	}
	// Check if static dir exists before serving
	if _, err := os.Stat(staticDir); err == nil {
		log.Printf("Serving static files from %s on /", staticDir)
		// Use StripPrefix to serve files correctly from the root URL path '/'
		// For example, request to '/' serves staticDir/index.html
		// Request to '/css/style.css' serves staticDir/css/style.css
		fs := http.FileServer(http.Dir(staticDir))
		mux.Handle("/", http.StripPrefix("/", fs))
	} else if errors.Is(err, os.ErrNotExist) {
		log.Printf("Static directory '%s' not found, skipping static file serving.", staticDir)
		// Optional: Add a default handler for '/' if no static files
		// mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		//     if r.URL.Path == "/" {
		//         http.NotFound(w, r) // Or provide a default welcome message
		//     } else {
		//         http.NotFound(w, r)
		//     }
		// })
	} else {
		log.Printf("Error checking static directory '%s': %v. Skipping static file serving.", staticDir, err)
	}

	// 7. Configure HTTP Server
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,              // Use the mux we defined
		ReadTimeout:  10 * time.Second, // Increased slightly
		WriteTimeout: 15 * time.Second, // Increased slightly
		IdleTimeout:  60 * time.Second,
	}

	// 8. Start Server with Graceful Shutdown
	go func() {
		log.Printf("Server listening on port %s", cfg.Port)
		log.Printf("WebSocket endpoint: ws://localhost:%s/ws", cfg.Port)
		log.Printf("API endpoints: http://localhost:%s/api/alerts/", cfg.Port)
		log.Printf("Health check: http://localhost:%s/healthz", cfg.Port)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Could not listen on %s: %v\n", cfg.Port, err)
		}
		log.Println("Server stopped listening.")
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit // Block until signal is received
	log.Printf("Shutdown signal (%s) received, initiating graceful shutdown...", sig)

	// Create context with timeout for shutdown
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second) // Increased timeout
	defer cancelShutdown()

	// Attempt graceful shutdown
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown failed: %v. Forcing exit.", err)
		// We might want to force close DB here if shutdown failed uncleanly
		_ = db.Close() // Attempt to close DB even on unclean HTTP shutdown
		os.Exit(1)     // Exit with error code
	}

	log.Println("Server exiting gracefully.")
}

// addTestData adds sample alerts to the database.
func addTestData(db *storage.DB, websocketHub *hub.Hub) {
	log.Println("Adding test data...")
	testAlerts := []model.Alert{
		{
			ID:          uuid.NewString(),
			Type:        "order_error",
			Severity:    3,
			Timestamp:   time.Now().UTC().Add(-5 * time.Minute),
			Title:       "Incorrect Item Shipped",
			Message:     "Order #12345 shipped with item SKU-B instead of SKU-A.",
			Source:      "OrderSystem",
			ReferenceID: "12345",
			Status:      "new",
		},
		{
			ID:          uuid.NewString(),
			Type:        "complaint",
			Severity:    2,
			Timestamp:   time.Now().UTC().Add(-10 * time.Minute),
			Title:       "Late Delivery Complaint",
			Message:     "Customer C789 reported delivery for order #67890 was 2 days late.",
			Source:      "CRM",
			ReferenceID: "C789",
			Status:      "investigating",
		},
		{
			ID:        uuid.NewString(),
			Type:      "system",
			Severity:  1,
			Timestamp: time.Now().UTC().Add(-2 * time.Minute),
			Title:     "Scheduled Maintenance",
			Message:   "Billing service will undergo maintenance tonight 2-3 AM UTC.",
			Source:    "Ops",
			Status:    "new",
		},
		{
			ID:          uuid.NewString(),
			Type:        "security",
			Severity:    4,
			Timestamp:   time.Now().UTC().Add(-1 * time.Minute),
			Title:       "Potential Brute Force Attack",
			Message:     "Multiple failed login attempts detected for user 'admin'.",
			Source:      "AuthService",
			Status:      "new",
			ReferenceID: "admin",
		},
	}

	ctx := context.Background() // Use background context for startup task
	for _, alert := range testAlerts {
		// Use SaveAlert which handles insert/update
		if err := db.SaveAlert(ctx, alert); err != nil {
			log.Printf("Error adding/updating test alert %s: %v", alert.ID, err)
		} else {
			log.Printf("Added/Updated test alert: %s", alert.String())
			// Broadcast test data to any connected clients
			websocketHub.Broadcast <- alert
		}
	}
	log.Println("Test data addition complete.")
}
