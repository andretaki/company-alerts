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

	_ "github.com/lib/pq"                              // Postgres driver - underscore means import for side effects (registering driver)
	"github.com/yourorg/company-alerts/internal/model" // <--- CORRECTED: Use the central model package
)

const (
	pingTimeout   = 5 * time.Second  // Timeout for initial DB ping
	execTimeout   = 5 * time.Second  // Timeout for INSERT/UPDATE/DELETE
	queryTimeout  = 10 * time.Second // Timeout for SELECT queries
	schemaTimeout = 15 * time.Second // Increased timeout for potential CREATE TABLE/INDEX
)

// Open establishes DB connection, pings, and ensures schema.
func Open() (*sql.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL environment variable is not set or empty")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open failed: %w", err)
	}

	// Configure connection pool for better performance and resource management
	db.SetMaxOpenConns(10)                 // Max number of open connections
	db.SetMaxIdleConns(5)                  // Max number of idle connections
	db.SetConnMaxLifetime(5 * time.Minute) // Max lifetime of a connection
	db.SetConnMaxIdleTime(2 * time.Minute) // Max time a connection can be idle

	// Verify connectivity with a timeout
	pingCtx, cancelPing := context.WithTimeout(context.Background(), pingTimeout)
	defer cancelPing()
	if err = db.PingContext(pingCtx); err != nil {
		db.Close() // Close unusable pool before returning error
		return nil, fmt.Errorf("db.PingContext failed (check DB server and credentials): %w", err)
	}
	log.Println("Database ping successful.")

	// Ensure the required schema exists
	if err = ensureSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ensure database schema: %w", err)
	}
	log.Println("Database schema ('alerts' table and indexes) verified/created.")

	return db, nil
}

// ensureSchema creates the table and indexes if they don't exist.
func ensureSchema(db *sql.DB) error {
	schemaSQL := `
	CREATE TABLE IF NOT EXISTS alerts (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		severity INT NOT NULL,
		timestamp BIGINT NOT NULL, -- Store as Unix Milliseconds UTC
		summary TEXT,
		detail TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp() -- Use clock_timestamp for higher precision insert time
	);
	CREATE INDEX IF NOT EXISTS idx_alerts_timestamp ON alerts (timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts (type);
	CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON alerts (created_at DESC);
	`
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	_, err := db.ExecContext(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("schema execution failed: %w", err)
	}
	return nil
}

// SaveAlert inserts an alert, using ON CONFLICT DO NOTHING for idempotency based on ID.
// --- CORRECTED: Accepts model.Alert ---
func SaveAlert(db *sql.DB, a model.Alert) error {
	query := `
	INSERT INTO alerts (id, type, severity, timestamp, summary, detail)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (id) DO NOTHING -- If alert ID already exists, do nothing.
	`
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	result, err := db.ExecContext(ctx, query,
		a.ID, a.Type, a.Severity, a.Timestamp, a.Summary, a.Detail)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("db.ExecContext timeout saving alert ID %s: %w", a.ID, err)
		}
		return fmt.Errorf("db.ExecContext failed for alert ID %s: %w", a.ID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err == nil && rowsAffected == 0 {
		log.Printf("Alert insert skipped (ID %s likely already exists).", a.ID)
	} else if err == nil {
		log.Printf("Alert ID %s saved successfully to database.", a.ID)
	}

	return nil
}

// LoadRecent retrieves the most recent alerts, ordered by the alert's timestamp.
// --- CORRECTED: Returns []model.Alert ---
func LoadRecent(db *sql.DB, limit int) ([]model.Alert, error) {
	if limit <= 0 {
		limit = 50 // Provide a sensible default/minimum limit
	}
	query := `
	SELECT id, type, severity, timestamp, summary, detail
	FROM alerts
	ORDER BY timestamp DESC, created_at DESC
	LIMIT $1
	`
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, query, limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("db.QueryContext timeout loading recent alerts: %w", err)
		}
		return nil, fmt.Errorf("db.QueryContext failed: %w", err)
	}
	defer rows.Close()

	// --- CORRECTED: Use model.Alert ---
	list := make([]model.Alert, 0, limit)
	for rows.Next() {
		// --- CORRECTED: Use model.Alert ---
		var a model.Alert
		if err := rows.Scan(&a.ID, &a.Type, &a.Severity, &a.Timestamp, &a.Summary, &a.Detail); err != nil {
			log.Printf("Error scanning alert row into struct: %v", err)
			continue
		}
		list = append(list, a)
	}

	if err = rows.Err(); err != nil {
		return list, fmt.Errorf("error during rows iteration: %w", err)
	}

	if ctx.Err() != nil {
		return list, fmt.Errorf("context deadline exceeded after processing rows: %w", ctx.Err())
	}

	return list, nil
}
