// /home/andre/company-alerts/internal/notifier/notifier.go
package notifier

import (
	_ "embed" // REQUIRED for //go:embed
	"fmt"
	"log"
	"os" // Needed for temp file handling
	// Needed for temp file handling
	"strings" // For formatting history
	"time"

	"github.com/gen2brain/beeep" // Use beeep for notifications
	"github.com/getlantern/systray"
	"github.com/yourorg/company-alerts/internal/model" // Use the central model
)

// Embed the icon file.
// IMPORTANT: This path is relative to THIS Go file's directory.
// Ensure `assets/icons8-system-report-80.png` exists inside the `internal/notifier` directory.
//
//go:embed assets/icons8-system-report-80.png
var iconBytes []byte // Will now hold the PNG data

// Global variable to store the path to the icon if extracted
var iconTempPath string

// RunTray initializes and runs the system tray icon. Returns channel closed on quit.
func RunTray(onHistory func()) <-chan struct{} {
	trayExitChan := make(chan struct{})

	// Attempt to extract embedded icon to a temporary file for beeep
	var err error
	iconTempPath, err = extractIcon() // Uses the new PNG data
	if err != nil {
		log.Printf("WARNING: Failed to extract embedded icon to temporary file: %v. Notifications may not show icon.", err)
		if iconTempPath != "" {
			_ = os.Remove(iconTempPath)
			iconTempPath = ""
		}
	} else if iconTempPath != "" {
		log.Printf("Icon extracted to temporary path: %s", iconTempPath)
		// Clean up the temp file on exit
		defer os.Remove(iconTempPath)
	}

	// systray.Run must run in its own dedicated OS thread.
	go func() {
		onReady := func() {
			log.Println("Systray ready.")
			if len(iconBytes) == 0 {
				log.Println("WARNING: Embedded icon data is empty. Systray icon may not display correctly. Ensure 'internal/notifier/assets/icons8-system-report-80.png' exists.")
				systray.SetTitle("⚠️ Alerts")
				systray.SetTooltip("Company Alerts Client (Icon Missing)")
			} else {
				// systray should handle PNG data directly
				systray.SetIcon(iconBytes)
				systray.SetTitle("Alerts")
				systray.SetTooltip("Company Alerts Client")
			}

			mHistory := systray.AddMenuItem("View History", "Show recent alerts (logs to console)")
			systray.AddSeparator()
			mQuit := systray.AddMenuItem("Quit", "Exit the application")

			go func() {
				for {
					select {
					case <-mHistory.ClickedCh:
						log.Println("History menu item clicked.")
						if onHistory != nil {
							onHistory()
						} else {
							log.Println("No history callback provided.")
						}
					case <-mQuit.ClickedCh:
						log.Println("Quit menu item clicked. Requesting systray exit...")
						systray.Quit()
						return
					}
				}
			}()
			log.Println("Systray menu configured.")
		}

		onExit := func() {
			log.Println("Systray exiting.")
			close(trayExitChan)
			// Attempt cleanup of temp icon again
			if iconTempPath != "" {
				_ = os.Remove(iconTempPath)
			}
		}

		systray.Run(onReady, onExit)
		log.Println("Systray Run() function finished.")
	}()

	return trayExitChan
}

// extractIcon attempts to write the embedded icon bytes to a temporary file
// and returns the path. Uses .png extension now.
func extractIcon() (string, error) {
	if len(iconBytes) == 0 {
		return "", nil // No icon bytes to extract
	}

	// Create a temporary file with .png extension
	tmpfile, err := os.CreateTemp("", "company-alerts-icon-*.png") // Use .png extension
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for icon: %w", err)
	}
	// Ensure closure if we exit early due to write error
	defer tmpfile.Close()

	if _, err := tmpfile.Write(iconBytes); err != nil {
		// Attempt to clean up before returning error
		_ = os.Remove(tmpfile.Name())
		return "", fmt.Errorf("failed to write icon bytes to temp file %s: %w", tmpfile.Name(), err)
	}

	// Close the file explicitly before returning its name
	if err := tmpfile.Close(); err != nil {
		// Attempt to clean up before returning error
		_ = os.Remove(tmpfile.Name())
		return "", fmt.Errorf("failed to close temp icon file %s: %w", tmpfile.Name(), err)
	}

	// Return the path to the temporary file
	return tmpfile.Name(), nil
}

// --- Notification Functions using beeep ---

// PushToast sends a desktop notification using the Alert's String() method for the title.
func PushToast(a model.Alert) error {
	// Pass the path to the extracted temp PNG icon file.
	err := beeep.Notify(a.String(), strings.TrimSpace(a.Detail), iconTempPath) // Use iconTempPath (which is now a .png)
	if err != nil {
		log.Printf("beeep notification failed for alert ID %s: %v", a.ID, err)
		return fmt.Errorf("beeep.Notify failed: %w", err)
	}
	log.Printf("Toast notification pushed via beeep for alert ID %s", a.ID)
	return nil
}

// ShowInfo displays a simple informational toast notification.
func ShowInfo(message string) {
	// Use beeep.Notify for informational messages.
	err := beeep.Notify("Information", message, iconTempPath) // Use iconTempPath
	if err != nil {
		log.Printf("Failed to push info beeep: %v", err)
	}
}

// ShowError displays an error toast notification.
func ShowError(message string) {
	// Use beeep.Alert for error messages, which might use a different OS style.
	err := beeep.Alert("Error", message, iconTempPath) // Use iconTempPath
	if err != nil {
		log.Printf("Failed to push error beeep: %v", err)
	}
}

// --- History Display Function (Unchanged logic, uses corrected ShowInfo) ---

// ShowHistory displays recent alerts (currently logs to console and shows an info toast).
func ShowHistory(alerts []model.Alert) { // Use model.Alert
	count := len(alerts)
	log.Printf("--- Displaying Alert History (%d alerts) ---", count)

	if count == 0 {
		log.Println("No history entries found.")
		ShowInfo("No alert history is available.") // Uses corrected ShowInfo
		return
	}

	var historyLog strings.Builder
	fmt.Fprintf(&historyLog, "Recent Alerts (%d):\n", count)
	for i, a := range alerts {
		log.Printf("  %d: [%s] ID:%s Type:%s Sev:%d Sum:'%s'",
			i+1,
			a.Time().Format(time.RFC3339),
			a.ID,
			a.Type,
			a.Severity,
			a.Summary,
		)
		fmt.Fprintf(&historyLog, "%d: %s [%s] %s\n", i+1, a.Time().Format("15:04:05"), a.Type, a.Summary)
	}
	log.Println("--- End of Alert History ---")

	ShowInfo(fmt.Sprintf("History requested. %d alerts logged to console.", count)) // Uses corrected ShowInfo
}
