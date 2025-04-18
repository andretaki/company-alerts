// /home/andre/company-alerts/cmd/alerts-client/main.go
package main

import (
	"context" // Needed for signals, DB operations, network
	"log"
	"os"        // Needed for signals, env vars
	"os/signal" // Needed for graceful shutdown
	"syscall"   // Needed for SIGTERM

	"github.com/yourorg/company-alerts/internal/config"   // Needed
	"github.com/yourorg/company-alerts/internal/model"    // Use the central model
	"github.com/yourorg/company-alerts/internal/network"  // Needed
	"github.com/yourorg/company-alerts/internal/notifier" // Needed
	"github.com/yourorg/company-alerts/internal/storage"  // Needed
)

func main() {
	// Use standard log flags for timestamp and file:line info
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Company Alerts client...")

	// 1 – Load configuration (handles file and env var token override)
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}
	log.Println("Configuration loaded successfully.")
	// Log sensitive info carefully. Avoid logging the token itself.
	log.Printf("Config: ServerURL=%s, UseWebSocket=%t, PollInterval=%v, MinSeverity=%d, NotifyTypes=%v",
		cfg.ServerURL, cfg.UseWebSocket, cfg.PollInterval, cfg.MinSeverity, cfg.NotifyTypes)

	// 2 – Initialize Database Connection (includes ping and schema check)
	db, err := storage.Open()
	if err != nil {
		log.Fatalf("FATAL: Failed to connect to database: %v", err)
	}
	defer func() {
		log.Println("Closing database connection...")
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		} else {
			log.Println("Database connection closed.")
		}
	}()
	log.Println("Database connection established and schema verified.")

	// 3 – Setup Graceful Shutdown Context
	// This context listens for OS signals (Interrupt, Terminate)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Calling stop() ensures that resources associated with the context are released.
	// It's also used to signal other parts of the application to shut down.
	defer stop()
	log.Println("Graceful shutdown handler registered.")

	// 4 – Initialize System Tray UI
	// RunTray starts the systray in a goroutine and returns a channel
	// that is closed when the user clicks "Quit" in the tray menu.
	trayDone := notifier.RunTray(func() {
		// This callback function is executed when "View History" is clicked.
		log.Println("Executing 'View History' action from systray.")
		alerts, err := storage.LoadRecent(db, 100) // Load last 100 alerts - returns []model.Alert
		if err != nil {
			log.Printf("Error loading alert history for systray: %v", err)
			notifier.ShowError("Failed to load history from database.")
		} else {
			// ShowHistory currently logs to console and shows an info toast.
			notifier.ShowHistory(alerts)
		}
	})
	log.Println("System tray initialized.")

	// 5 – Setup Network Listener (WebSocket or HTTP Polling)
	// Create a buffered channel to receive alerts from the network goroutine.
	// Buffer size allows network receiver to queue alerts if main loop is busy.
	// Adjust size based on expected alert frequency and processing time.
	incomingAlerts := make(chan model.Alert, 32) // Increased buffer size slightly
	go network.Listen(ctx, cfg, incomingAlerts)  // Starts WS or HTTP listener
	// Note: network.Listen logs its own startup message including the mode.

	log.Println("Application main loop started. Listening for events...")

	// 6 - Main Application Event Loop
	// This loop waits for signals: context cancellation (OS signal),
	// systray quit, or new alerts from the network.
	for {
		select {
		case <-ctx.Done():
			// OS signal received (SIGINT/SIGTERM) or stop() called explicitly.
			log.Println("Shutdown signal (context done) received, initiating clean exit...")
			// No need to call stop() here, defer stop() handles it.
			// Goroutines using ctx should detect cancellation and exit.
			// Allow deferred functions (like db.Close) to run.
			return // Exit main func

		case <-trayDone:
			// User clicked "Quit" in the system tray menu.
			log.Println("Systray quit signal received, initiating clean exit...")
			// Explicitly call stop() to cancel the main context.
			// This signals other goroutines (like network listeners) to shut down.
			stop()
			// The loop will likely iterate once more and exit via the <-ctx.Done() case.
			// Or we can return here directly, defer stop() still runs.
			return // Exit main func

		case alert, ok := <-incomingAlerts:
			// Received an alert from the network listener goroutine.
			if !ok {
				// The incomingAlerts channel was closed by the network listener.
				// This usually indicates a non-recoverable error in the listener or
				// that it shut down gracefully after context cancellation.
				log.Println("Network alert channel closed, initiating exit...")
				stop() // Ensure context is cancelled if not already
				return // Exit main func
			}

			// Process the received alert
			log.Printf("Processing received alert: ID=%s Type=%s Severity=%d", alert.ID, alert.Type, alert.Severity)

			// Filter based on configuration rules
			if !cfg.ShouldNotify(alert) { // Pass the whole alert struct
				log.Printf("Alert ID=%s (Type:%s, Sev:%d) filtered out by config rules.", alert.ID, alert.Type, alert.Severity)
				continue // Skip to the next event
			}

			// Send Desktop Notification (Toast)
			if err := notifier.PushToast(alert); err != nil {
				// Log failure, but continue processing (e.g., still save to DB)
				log.Printf("ERROR: Failed to push toast notification for alert ID=%s: %v", alert.ID, err)
				// Optionally, show an error in the systray or a generic error toast?
				// notifier.ShowError("Failed to display notification for alert " + alert.ID)
			} else {
				log.Printf("Successfully pushed toast for alert ID=%s.", alert.ID)
			}

			// Save Alert to Database - storage.SaveAlert now takes model.Alert
			if err := storage.SaveAlert(db, alert); err != nil {
				// Log failure, potentially critical if persistence is required.
				log.Printf("ERROR: Failed to save alert ID=%s to database: %v", alert.ID, err)
				// Consider retry logic or more prominent error reporting here for DB errors.
			}
			// Success message is logged within SaveAlert itself now.
		}
	}
}
