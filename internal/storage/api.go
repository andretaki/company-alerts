// internal/storage/api.go
package storage

import (
	"context"

	"github.com/yourorg/company-alerts/internal/model"
)

// Package-level compatibility functions that match the signatures expected in main.go

// SaveAlert saves an alert to the database using the default connection
func SaveAlert(db *DB, alert model.Alert) error {
	// Use current context as a fallback
	return db.SaveAlert(context.Background(), alert)
}

// LoadRecent loads the most recent alerts from the database
func LoadRecent(db *DB, limit int) ([]model.Alert, error) {
	// Create a basic filter that just limits by count and sorts by timestamp
	filter := Filter{
		Limit: limit,
	}
	return db.ListAlerts(context.Background(), filter)
}
