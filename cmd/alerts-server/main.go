package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Import server packages
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/handlers"
	"github.com/yourorg/company-alerts/internal/server/hub"
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
		log.Println("WARNING: Server starting with NO allowed tokens. No clients can authenticate via config.")
	}

	// 2. Create and Run WebSocket Hub
	websocketHub := hub.NewHub()
	go websocketHub.Run() // Run the hub's event loop in the background

	// 3. Initialize alert store and handlers
	alertStore := handlers.NewInMemoryAlertStore()
	alertsHandler := handlers.NewAlertsHandler(alertStore, websocketHub, cfg)

	// 4. Setup HTTP Server and Routes
	mux := http.NewServeMux()

	// WebSocket endpoint - Requires token in query param: ws://.../ws?token=...
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handlers.ServeWs(cfg, websocketHub, w, r)
	})

	// Alert API endpoints
	alertsHandler.RegisterAlertRoutes(mux)

	// Simple health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Static file handling (for client UI if needed)
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "./static"
	}
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("/", fs)

	// 5. Configure HTTP Server
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux, // Use the mux we defined
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 6. Start Server with Graceful Shutdown
	// Run server in a goroutine so that it doesn't block.
	go func() {
		log.Printf("Server listening on port %s", cfg.Port)
		log.Printf("WebSocket endpoint: ws://localhost:%s/ws", cfg.Port)
		log.Printf("API endpoint: http://localhost:%s/api/alerts", cfg.Port)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Could not listen on %s: %v\n", cfg.Port, err)
		}
		log.Println("Server stopped")
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit // Block until signal is received
	log.Println("Shutdown signal received, initiating graceful shutdown...")

	// The context is used to inform the server it has 5 seconds to finish
	// the requests it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exiting")
}
