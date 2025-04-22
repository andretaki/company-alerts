// internal/notifier/notifier.go
package notifier

import (
	"context" // Needed for API call
	_ "embed"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gen2brain/beeep"
	"github.com/getlantern/systray"
	"github.com/yourorg/company-alerts/internal/config"  // Need config for API call
	"github.com/yourorg/company-alerts/internal/model"   // Unified model
	"github.com/yourorg/company-alerts/internal/network" // Need for FetchHistory
)

//go:embed assets/icons8-system-report-80.png
var iconBytes []byte
var iconTempPath string

// RunTray initializes and runs the system tray icon.
// Now takes client config needed for FetchHistory API call.
// Returns a channel that is closed when the user clicks "Quit".
func RunTray(cfg *config.Config) <-chan struct{} {
	trayExitChan := make(chan struct{}) // Channel to signal exit

	// Attempt to extract embedded icon to a temporary file for beeep/systray
	var err error
	iconTempPath, err = extractIcon()
	if err != nil {
		log.Printf("WARNING: Failed to extract embedded icon: %v. Notifications may not show icon.", err)
		// Don't try to clean up path if extraction failed
		iconTempPath = "" // Ensure path is empty on error
	} else if iconTempPath != "" {
		log.Printf("Icon extracted to temporary path: %s", iconTempPath)
		// systray's onExit callback will handle cleanup now.
	}

	// systray.Run must run in its own dedicated OS thread.
	go func() {
		// onReady is called when the systray is ready to be interacted with.
		onReady := func() {
			log.Println("Systray ready.")
			if len(iconBytes) == 0 {
				log.Println("WARNING: Embedded icon data is empty. Systray icon may not display correctly.")
				systray.SetTitle("⚠️ Alerts") // Use a visible indicator if icon fails
				systray.SetTooltip("Company Alerts Client (Icon Missing)")
			} else {
				// systray SetIcon should handle PNG data directly.
				systray.SetIcon(iconBytes)
				systray.SetTitle("Alerts") // Tooltip text
				systray.SetTooltip("Company Alerts Client")
			}

			// Add menu items
			mHistory := systray.AddMenuItem("View History", "Fetch recent alerts from server")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("Quit", "Exit the application")

			// Goroutine to handle menu item clicks
			go func() {
				for {
					select {
					case <-mHistory.ClickedCh:
						log.Println("History menu item clicked.")
						// Fetch history from API in a separate goroutine
						// to avoid blocking the systray menu handling loop.
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							defer cancel()
							// Fetch a reasonable number of recent alerts, e.g., 50
							// Could make this limit configurable via ALERTS_HISTORY_FETCH_LIMIT env var
							fetchLimit := 50
							alerts, err := network.FetchHistory(ctx, cfg, fetchLimit)
							if err != nil {
								log.Printf("Error fetching alert history from API: %v", err)
								ShowError("Failed to fetch history from server.") // Show error in toast
							} else {
								ShowHistory(alerts) // Display fetched history (logs & shows toast)
							}
						}()

					case <-mQuit.ClickedCh:
						log.Println("Quit menu item clicked. Requesting application exit...")
						// systray.Quit() triggers the onExit callback below.
						// The onExit callback closes trayExitChan, signaling main loop to stop.
						systray.Quit()
						return // Exit the menu handling goroutine
					}
				}
			}() // End of menu click handler goroutine

			log.Println("Systray menu configured.")
		} // End of onReady

		// onExit is called when systray.Quit() is called.
		onExit := func() {
			log.Println("Systray exiting.")
			// Clean up the temporary icon file if it was created.
			if iconTempPath != "" {
				err := os.Remove(iconTempPath)
				if err != nil {
					log.Printf("Warning: Failed to remove temporary icon file '%s': %v", iconTempPath, err)
				} else {
					log.Printf("Removed temporary icon file: %s", iconTempPath)
				}
			}
			// Close the channel to signal the main application loop that the user has quit.
			close(trayExitChan)
		} // End of onExit

		// Run the systray event loop. This blocks until systray.Quit() is called.
		systray.Run(onReady, onExit)
		log.Println("Systray Run() function finished.") // Should only log after Quit is clicked

	}() // End of main systray goroutine

	return trayExitChan // Return the channel to the main application loop
}

// extractIcon attempts to write the embedded icon bytes to a temporary file
// and returns the path. Returns an empty string and logs warning on failure.
func extractIcon() (string, error) {
	if len(iconBytes) == 0 {
		return "", fmt.Errorf("embedded icon data is empty") // Return error if no data
	}

	// Create a temporary file with a specific prefix and .png extension
	tmpfile, err := os.CreateTemp("", "company-alerts-icon-*.png")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for icon: %w", err)
	}

	// Write the embedded bytes to the temp file
	if _, err := tmpfile.Write(iconBytes); err != nil {
		// If write fails, attempt to close and remove the temp file before returning error
		tmpfile.Close()               // Ignore close error
		_ = os.Remove(tmpfile.Name()) // Ignore remove error
		return "", fmt.Errorf("failed to write icon bytes to temp file %s: %w", tmpfile.Name(), err)
	}

	// Close the file explicitly to ensure data is flushed and descriptor is released
	if err := tmpfile.Close(); err != nil {
		// If close fails, attempt to remove the temp file before returning error
		_ = os.Remove(tmpfile.Name()) // Ignore remove error
		return "", fmt.Errorf("failed to close temp icon file %s: %w", tmpfile.Name(), err)
	}

	// Return the path to the successfully created and closed temporary file
	return tmpfile.Name(), nil
}

// --- Notification Functions using beeep ---

// PushToast sends a desktop notification using the unified model.Alert.
// It formats the title and message and chooses the notification type based on severity.
func PushToast(a model.Alert) error {
	// Construct a clear title
	title := fmt.Sprintf("[%s] %s (Sev %d)", strings.ToUpper(a.Type), a.Title, a.Severity)
	// Use the full message, but trim whitespace
	message := strings.TrimSpace(a.Message)
	// Beeep might truncate long messages itself, but good practice to be concise.
	// if len(message) > 250 {
	//     message = message[:247] + "..."
	// }

	// Use beeep.Alert for higher severity, beeep.Notify for lower.
	// Pass the path to the extracted temp PNG icon file (iconTempPath).
	var err error
	switch {
	case a.Severity >= 3: // High or Critical
		err = beeep.Alert(title, message, iconTempPath) // Uses system's alert style
	default: // Medium, Low, or unspecified (< 3)
		err = beeep.Notify(title, message, iconTempPath) // Uses system's notification style
	}

	if err != nil {
		// Log the failure but return the error for the main loop to know.
		log.Printf("ERROR: beeep notification failed for alert ID %s: %v", a.ID, err)
		return fmt.Errorf("beeep failed: %w", err)
	}
	// Log success only if needed for verbose debugging
	// log.Printf("Toast notification pushed via beeep for alert ID %s", a.ID)
	return nil
}

// ShowInfo displays a simple informational toast notification.
func ShowInfo(message string) {
	// Use beeep.Notify for standard informational messages.
	err := beeep.Notify("Information", message, iconTempPath) // Use iconTempPath
	if err != nil {
		log.Printf("Failed to push info beeep: %v", err)
	}
}

// ShowError displays an error toast notification.
func ShowError(message string) {
	// Use beeep.Alert for error messages, often styled more prominently by the OS.
	err := beeep.Alert("Error", message, iconTempPath) // Use iconTempPath
	if err != nil {
		log.Printf("Failed to push error beeep: %v", err)
	}
}

// --- History Display Function ---

// ShowHistory logs fetched alerts to the console and shows an info toast.
func ShowHistory(alerts []model.Alert) { // Takes slice of unified model.Alert
	count := len(alerts)
	log.Printf("--- Displaying Alert History (%d alerts fetched) ---", count)

	if count == 0 {
		log.Println("No history entries found.")
		ShowInfo("No recent alert history found.") // Inform user via toast
		return
	}

	// Use a string builder if you need the formatted history as a single string elsewhere.
	// var historyLog strings.Builder
	// fmt.Fprintf(&historyLog, "Recent Alerts (%d):\n", count)

	// Log each alert with relevant details. Log in reverse order (newest first).
	for i := 0; i < count; i++ {
		a := alerts[i] // Assumes API returns newest first
		log.Printf("  %d: [%s] ID:%s Type:%s Sev:%d Status:'%s' Title:'%s'",
			i+1,
			a.Timestamp.Local().Format(time.DateTime), // Show timestamp in local time for readability
			a.ID,
			a.Type,
			a.Severity,
			a.Status, // Include status if available
			a.Title,
		)
		// Append to string builder if needed:
		// fmt.Fprintf(&historyLog, "%d: %s [%s] %s\n",
		//     i+1, a.Timestamp.Local().Format("15:04:05"), a.Type, a.Title)
	}
	log.Println("--- End of Alert History ---")

	// Show a summary toast indicating history was logged.
	ShowInfo(fmt.Sprintf("Fetched %d alerts. See console log for details.", count))
}
