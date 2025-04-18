// /home/andre/company-alerts/internal/notifier/notifier.go
package notifier

import (
	_ "embed" // REQUIRED for //go:embed
	"fmt"
	"log"
	"strings" // For formatting history
	"time"

	"github.com/gen2brain/beeep" // <--- Use beeep for notifications
	"github.com/getlantern/systray"
	"github.com/yourorg/company-alerts/internal/model" // Use the central model
)

// Embed the icon file.
// IMPORTANT: This path is relative to THIS Go file's directory.
// Ensure `assets/app.ico` exists inside the `internal/notifier` directory.
//
//go:embed assets/app.ico
var iconBytes []byte

// RunTray initializes and runs the system tray icon. Returns channel closed on quit.
func RunTray(onHistory func()) <-chan struct{} {
	trayExitChan := make(chan struct{})

	// systray.Run must run in its own dedicated OS thread.
	// Launching it in a goroutine is standard practice.
	go func() {
		// onReady is called when the systray is initialized successfully.
		onReady := func() {
			log.Println("Systray ready.")
			if len(iconBytes) == 0 {
				// This indicates the embedding failed or the file is empty.
				log.Println("WARNING: Embedded icon data is empty. Systray icon may not display correctly. Ensure 'internal/notifier/assets/app.ico' exists and the build process includes embedding.")
				systray.SetTitle("⚠️ Alerts") // Indicate missing icon in title
				systray.SetTooltip("Company Alerts Client (Icon Missing)")
			} else {
				// Note: systray.SetIcon itself might require specific OS handling for .ico on Linux.
				// It might expect PNG. If the icon doesn't show, try converting app.ico to app.png.
				systray.SetIcon(iconBytes) // Set the icon from embedded bytes
				systray.SetTitle("Alerts")
				systray.SetTooltip("Company Alerts Client")
			}

			// --- Menu Items ---
			mHistory := systray.AddMenuItem("View History", "Show recent alerts (logs to console)")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("Quit", "Exit the application")

			// --- Menu Item Click Handler Loop ---
			// Run this in a separate goroutine to avoid blocking onReady
			go func() {
				for {
					select {
					// Handle clicks on the "View History" menu item
					case <-mHistory.ClickedCh:
						log.Println("History menu item clicked.")
						if onHistory != nil {
							onHistory() // Execute the provided callback function
						} else {
							log.Println("No history callback provided.")
						}
					// Handle clicks on the "Quit" menu item
					case <-mQuit.ClickedCh:
						log.Println("Quit menu item clicked. Requesting systray exit...")
						systray.Quit() // Triggers the onExit callback eventually
						// We don't close trayExitChan here; onExit does that.
						return // Exit this click handler goroutine
					}
				}
			}()
			log.Println("Systray menu configured.")
		}

		// onExit is called when systray.Quit() has been called and the cleanup is done.
		onExit := func() {
			log.Println("Systray exiting.")
			// Signal that the systray has shut down by closing the channel.
			// The main application loop listens on this channel.
			close(trayExitChan)
		}

		// Start the platform-specific systray event loop. This is blocking.
		systray.Run(onReady, onExit)
		// Execution resumes here only after systray has fully exited.
		log.Println("Systray Run() function finished.")
	}()

	// Return the channel immediately; the goroutine above handles the setup.
	return trayExitChan
}

// --- Notification Functions using beeep ---

// PushToast sends a desktop notification using the Alert's String() method for the title.
func PushToast(a model.Alert) error {
	// beeep.Notify(title, message, iconPath)
	// iconPath needs to be an actual path to an image file (e.g., PNG).
	// Embedding raw bytes doesn't work directly with most notification systems.
	// We'll use an empty string "" for now, which might show a default app icon or none.
	// Consider extracting the embedded icon to a temp file if displaying it is critical.
	assetPath := "" // Leave empty for now
	err := beeep.Notify(a.String(), strings.TrimSpace(a.Detail), assetPath)
	if err != nil {
		// Log the error here, as the main loop might just log failures too.
		log.Printf("beeep notification failed for alert ID %s: %v", a.ID, err)
		// Wrap the error for potentially more specific handling by the caller.
		return fmt.Errorf("beeep.Notify failed: %w", err)
	}
	log.Printf("Toast notification pushed via beeep for alert ID %s", a.ID)
	return nil
}

// ShowInfo displays a simple informational toast notification.
func ShowInfo(message string) {
	// beeep.Info(title, message, iconPath)
	assetPath := "" // Leave empty for now
	err := beeep.Info("Information", message, assetPath)
	if err != nil {
		log.Printf("Failed to push info beeep: %v", err)
	}
}

// ShowError displays an error toast notification.
func ShowError(message string) {
	// beeep.Error(title, message, iconPath)
	assetPath := "" // Leave empty for now
	err := beeep.Error("Error", message, assetPath)
	if err != nil {
		// Log failure to show the error toast itself.
		log.Printf("Failed to push error beeep: %v", err)
	}
}

// --- History Display Function (Unchanged other than type) ---

// ShowHistory displays recent alerts (currently logs to console and shows an info toast).
func ShowHistory(alerts []model.Alert) { // Use model.Alert
	count := len(alerts)
	log.Printf("--- Displaying Alert History (%d alerts) ---", count)

	if count == 0 {
		log.Println("No history entries found.")
		// Provide feedback to the user via a toast notification.
		ShowInfo("No alert history is available.") // Use the new ShowInfo
		return
	}

	// Log each alert's details. Replace with actual UI display later.
	var historyLog strings.Builder
	fmt.Fprintf(&historyLog, "Recent Alerts (%d):\n", count)
	for i, a := range alerts {
		// Use a consistent, readable time format (ISO 8601 / RFC3339 is good)
		log.Printf("  %d: [%s] ID:%s Type:%s Sev:%d Sum:'%s'",
			i+1,
			a.Time().Format(time.RFC3339), // Use model.Alert's Time() method
			a.ID,
			a.Type,
			a.Severity,
			a.Summary,
		)
		// Append to string builder for potential future use (e.g., showing in a dialog)
		// Limiting detail length here if necessary.
		fmt.Fprintf(&historyLog, "%d: %s [%s] %s\n", i+1, a.Time().Format("15:04:05"), a.Type, a.Summary)
	}
	log.Println("--- End of Alert History ---")

	// Show a confirmation toast that the history was requested/logged.
	// Could potentially show the 'historyLog' content in a basic message box later.
	ShowInfo(fmt.Sprintf("History requested. %d alerts logged to console.", count)) // Use the new ShowInfo
}
