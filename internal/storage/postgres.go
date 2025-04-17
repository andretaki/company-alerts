// /home/andre/company-alerts/internal/storage/postgres.go
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq" // Will be resolved by `go mod tidy`
	"github.com/yourorg/company-alerts/internal/network"
)

const (
	pingTimeout   = 5 * time.Second
	execTimeout   = 5 * time.Second
	queryTimeout  = 10 * time.Second
	schemaTimeout = 15 * time.Second // Allow more time for CREATE TABLE/INDEX
)

// Open establishes DB connection, pings, and ensures schema.
func Open() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL environment variable is not set")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open failed: %w", err)
	}

	// Configure connection pool (optional but recommended)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute) // Close idle connections sooner

	// Verify connectivity
	pingCtx, cancelPing := context.WithTimeout(context.Background(), pingTimeout)
	defer cancelPing()
	if err = db.PingContext(pingCtx); err != nil {
		db.Close() // Close unusable pool
		return nil, fmt.Errorf("db.PingContext failed: %w", err)
	}
	log.Println("Database ping successful.")

	// Ensure schema exists
	if err = ensureSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ensure database schema: %w", err)
	}
	log.Println("'alerts' table schema verified/created.")

	return db, nil
}

// ensureSchema creates the table and indexes if they don't exist.
func ensureSchema(db *sql.DB) error {
	schemaSQL := `
	CREATE TABLE IF NOT EXISTS alerts (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		severity INT NOT NULL,
		timestamp BIGINT NOT NULL, -- Unix Milliseconds UTC
		summary TEXT,
		detail TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	-- Create indexes concurrently if possible, though usually fine on startup
	CREATE INDEX IF NOT EXISTS idx_alerts_timestamp ON alerts (timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts (type);
	CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON alerts (created_at DESC);
	`
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	_, err := db.ExecContext(ctx, schemaSQL)
	return err
}

// SaveAlert inserts an alert, ignoring conflicts on the ID.
func SaveAlert(db *sql.DB, a network.Alert) error {
	query := `
	INSERT INTO alerts (id, type, severity, timestamp, summary, detail)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (id) DO NOTHING
	`
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	result, err := db.ExecContext(ctx, query,
		a.ID, a.Type, a.Severity, a.Timestamp, a.Summary, a.Detail)
	if err != nil {
		return fmt.Errorf("db.ExecContext failed for alert ID %s: %w", a.ID, err)
	}

	rowsAffected, _ := result.RowsAffected() // Error checking RowsAffected() is optional here
	if rowsAffected == 0 {
		// This can happen on conflict or if the row somehow already existed with identical values (unlikely with PK)
		log.Printf("Alert insert skipped (likely conflict) for ID: %s", a.ID)
	}
	return nil
}

// LoadRecent retrieves the most recent alerts, ordered by timestamp.
func LoadRecent(db *sql.DB, limit int) ([]network.Alert, error) {
	if limit <= 0 {
		limit = 50 // Sensible default
	}
	query := `
	SELECT id, type, severity, timestamp, summary, detail
	FROM alerts
	ORDER BY timestamp DESC, created_at DESC -- Secondary sort for stability
	LIMIT $1
	`
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("db.QueryContext failed: %w", err)
	}
	defer rows.Close()

	var list []network.Alert
	for rows.Next() {
		var a network.Alert
		// Scan into the Alert struct fields
		if err := rows.Scan(&a.ID, &a.Type, &a.Severity, &a.Timestamp, &a.Summary, &a.Detail); err != nil {
			log.Printf("Error scanning alert row: %v", err)
			continue // Skip this row, try next
		}
		list = append(list, a)
	}

	// Check for errors during row iteration
	if err = rows.Err(); err != nil {
		return list, fmt.Errorf("error during rows iteration: %w", err) // Return potentially partial list and error
	}

	return list, nil
}
