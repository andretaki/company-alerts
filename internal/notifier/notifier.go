// /home/andre/company-alerts/internal/notifier/notifier.go
package notifier

import (
	// REQUIRED for //go:embed
	"fmt"
	"log"
	"time" // REQUIRED for time format in ShowHistory

	"github.com/getlantern/systray" // Will be resolved by `go mod tidy`
	"github.com/go-toast/toast"     // Will be resolved by `go mod tidy`
	"github.com/yourorg/company-alerts/internal/network"
)

// CORRECT EMBED PATH: Relative to this directory.
// Requires file: internal/notifier/assets/app.ico
//
//go:embed assets/app.ico
var iconBytes []byte

// RunTray initializes and runs the system tray icon. Returns channel closed on quit.
func RunTray(onHistory func()) <-chan struct{} {
	trayExitChan := make(chan struct{})

	go func() { // systray.Run must run in its own goroutine
		onReady := func() {
			log.Println("Systray ready.")
			if len(iconBytes) == 0 {
				log.Println("WARNING: Embedded icon data is empty. Ensure 'internal/notifier/assets/app.ico' exists and is embedded.")
			}
			systray.SetIcon(iconBytes) // Set icon data
			systray.SetTitle("Alerts")
			systray.SetTooltip("Company Alerts Client")

			// Menu items
			mHistory := systray.AddMenuItem("View History", "Show recent alerts")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("Quit", "Exit the application")

			// Menu item click handler loop
			go func() {
				for {
					select {
					case <-mHistory.ClickedCh:
						log.Println("History menu item clicked.")
						if onHistory != nil {
							onHistory() // Execute callback
						}
					case <-mQuit.ClickedCh:
						log.Println("Quit menu item clicked.")
						systray.Quit() // Request systray exit
						return         // Exit handler loop
					}
				}
			}()
		}

		onExit := func() {
			log.Println("Systray exiting.")
			close(trayExitChan) // Signal exit to main application
		}

		// Start the systray event loop (blocking)
		systray.Run(onReady, onExit)
	}()

	return trayExitChan
}

// PushToast sends a desktop notification.
func PushToast(a network.Alert) error {
	notification := toast.Notification{
		AppID:   "Company Alerts",
		Title:   a.String(), // Uses Alert's String() method for title
		Message: a.Detail,
		// Icon field usually expects a file PATH. Toast library doesn't seem
		// to support raw icon bytes directly. Icon display might depend
		// on OS finding an associated icon for the AppID or executable.
		// Icon: "icon.ico", // If you distribute icon alongside .exe
	}

	err := notification.Push()
	if err != nil {
		// Log error here as caller might ignore it
		log.Printf("Toast notification push failed for alert ID %s: %v", a.ID, err)
		return fmt.Errorf("toast.Push failed: %w", err)
	}
	log.Printf("Toast notification pushed for alert ID %s", a.ID)
	return nil
}

// ShowHistory logs recent alerts (placeholder for actual UI).
func ShowHistory(alerts []network.Alert) {
	count := len(alerts)
	log.Printf("--- Displaying Alert History (Stub - %d alerts) ---", count)
	if count == 0 {
		log.Println("No history to display.")
		ShowInfo("No alert history available.") // Provide feedback
		return
	}

	for i, a := range alerts {
		// Log details - replace with actual UI display if needed
		log.Printf("  %d: [%s] ID:%s Type:%s Sev:%d Sum:%s",
			i+1,
			a.Time().Format(time.RFC3339), // Use standard time format
			a.ID,
			a.Type,
			a.Severity,
			a.Summary,
		)
	}
	log.Println("--- End of Alert History ---")
	ShowInfo(fmt.Sprintf("History requested. %d alerts logged to console.", count))
}

// ShowInfo displays a simple informational toast.
func ShowInfo(message string) {
	notification := toast.Notification{AppID: "Company Alerts", Title: "Info", Message: message}
	err := notification.Push()
	if err != nil {
		log.Printf("Failed to push info toast: %v", err)
	}
}

// ShowError displays an error toast.
func ShowError(message string) {
	notification := toast.Notification{AppID: "Company Alerts", Title: "Error", Message: message}
	err := notification.Push()
	if err != nil {
		log.Printf("Failed to push error toast: %v", err)
	}
}
