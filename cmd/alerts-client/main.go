// /home/andre/company-alerts/cmd/alerts-client/main.go
package main

import (
	"context" // Needed for signals, DB operations, network
	"log"
	"os"        // Needed for signals, env vars
	"os/signal" // Needed for graceful shutdown
	"syscall"   // Needed for SIGTERM

	"github.com/yourorg/company-alerts/internal/config"   // Needed
	"github.com/yourorg/company-alerts/internal/network"  // Needed
	"github.com/yourorg/company-alerts/internal/notifier" // Needed
	"github.com/yourorg/company-alerts/internal/storage"  // Needed
)

func main() {
	log.Println("Starting Company Alerts client...")

	// 1 – Load JSON config shipped with app
	cfg, err := config.Load("config.json") // cfg is declared and used
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Println("Configuration loaded.")

	// 2 – Open Postgres via env DATABASE_URL
	db, err := storage.Open() // db is declared and used
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Database connection established.")

	// 3 – Graceful shutdown context (catches Interrupt and Terminate)
	// ctx and stop are declared and used
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 4 – Systray UI setup
	// trayDone is declared and used
	trayDone := notifier.RunTray(func() {
		alerts, err := storage.LoadRecent(db, 200) // Uses db
		if err != nil {
			log.Printf("Error loading alert history: %v", err)
			notifier.ShowError("Failed to load history.")
		} else {
			notifier.ShowHistory(alerts)
		}
	})

	// 5 – Network listener setup
	// incomingAlerts is declared and used
	incomingAlerts := make(chan network.Alert, 16)
	go network.Listen(ctx, cfg, incomingAlerts) // Uses ctx and cfg

	log.Println("Application started. Listening for alerts...")

	// 6 - Main event loop
	for {
		select {
		case <-ctx.Done(): // Uses ctx
			log.Println("Shutdown signal received, exiting...")
			return
		case <-trayDone: // Uses trayDone
			log.Println("Systray quit signal received, exiting...")
			stop() // Uses stop
			return
		case alert, ok := <-incomingAlerts: // Uses incomingAlerts
			if !ok {
				log.Println("Alert channel closed, exiting...")
				stop() // Uses stop
				return
			}
			log.Printf("Received alert: ID=%s Type=%s Severity=%d", alert.ID, alert.Type, alert.Severity)

			// Use the corrected ShouldNotify signature
			if !cfg.ShouldNotify(alert.Type, alert.Severity) { // Uses cfg
				log.Printf("Alert ID=%s filtered out.", alert.ID)
				continue
			}

			if err := notifier.PushToast(alert); err != nil {
				log.Printf("Failed to push toast notification for alert ID=%s: %v", alert.ID, err)
			}

			if err := storage.SaveAlert(db, alert); err != nil { // Uses db
				log.Printf("Failed to save alert ID=%s to database: %v", alert.ID, err)
			} else {
				log.Printf("Alert ID=%s saved successfully.", alert.ID)
			}
		}
	}
}
