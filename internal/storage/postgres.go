// internal/storage/postgres.go
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings" // For building filter queries
	"time"

	_ "github.com/lib/pq"                              // Postgres driver side effects
	"github.com/yourorg/company-alerts/internal/model" // Use the unified model
)

const (
	pingTimeout      = 5 * time.Second
	execTimeout      = 5 * time.Second
	queryTimeout     = 10 * time.Second
	schemaTimeout    = 15 * time.Second
	defaultListLimit = 50
	maxListLimit     = 1000
)

// DB holds the database connection pool (*sql.DB).
type DB struct {
	*sql.DB
}

// Open establishes DB connection, pings, and ensures schema.
// This is the single public Open function for the storage package.
func Open() (*DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("DATABASE_URL environment variable is not set or empty")
	}

	dbPool, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open failed: %w", err)
	}

	dbPool.SetMaxOpenConns(15)
	dbPool.SetMaxIdleConns(5)
	dbPool.SetConnMaxLifetime(5 * time.Minute)
	dbPool.SetConnMaxIdleTime(2 * time.Minute)

	pingCtx, cancelPing := context.WithTimeout(context.Background(), pingTimeout)
	defer cancelPing()
	if err = dbPool.PingContext(pingCtx); err != nil {
		dbPool.Close()
		return nil, fmt.Errorf("db.PingContext failed (check DB server, network, and credentials): %w", err)
	}
	log.Println("INFO: Database ping successful.")

	db := &DB{dbPool}

	if err = db.ensureSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ensure database schema: %w", err)
	}
	log.Println("INFO: Database schema ('alerts' table and indexes) verified/created.")

	return db, nil
}

// ensureSchema creates the 'alerts' table and necessary indexes.
func (db *DB) ensureSchema(ctx context.Context) error {
	schemaSQL := `
	CREATE TABLE IF NOT EXISTS alerts (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		severity INT NOT NULL,
		timestamp TIMESTAMPTZ NOT NULL,
		title TEXT NOT NULL,
		message TEXT NOT NULL,
		source TEXT,
		reference_id TEXT,
		status TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
	);
	CREATE INDEX IF NOT EXISTS idx_alerts_timestamp ON alerts (timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts (type);
	CREATE INDEX IF NOT EXISTS idx_alerts_severity ON alerts (severity DESC);
	CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts (status);
	CREATE INDEX IF NOT EXISTS idx_alerts_source ON alerts (source);
	CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON alerts (created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_alerts_reference_id ON alerts (reference_id);
	CREATE OR REPLACE FUNCTION update_updated_at_column()
	RETURNS TRIGGER AS $$
	BEGIN
	   IF row(NEW.*) IS DISTINCT FROM row(OLD.*) THEN
	      NEW.updated_at = clock_timestamp();
	   END IF;
	   RETURN NEW;
	END;
	$$ language 'plpgsql';
	DROP TRIGGER IF EXISTS trigger_update_alerts_updated_at ON alerts;
	CREATE TRIGGER trigger_update_alerts_updated_at
	BEFORE UPDATE ON alerts
	FOR EACH ROW
	EXECUTE FUNCTION update_updated_at_column();
	`
	schemaCtx, cancel := context.WithTimeout(ctx, schemaTimeout)
	defer cancel()
	_, err := db.ExecContext(schemaCtx, schemaSQL)
	if err != nil {
		return fmt.Errorf("schema execution failed: %w", err)
	}
	return nil
}

// SaveAlert inserts a new alert or updates an existing one based on the ID (upsert).
func (db *DB) SaveAlert(ctx context.Context, a model.Alert) error {
	if a.Timestamp.IsZero() {
		a.Timestamp = time.Now().UTC()
	} else {
		a.Timestamp = a.Timestamp.UTC()
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("invalid alert data for save: %w", err)
	}

	query := `
	INSERT INTO alerts (id, type, severity, timestamp, title, message, source, reference_id, status)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	ON CONFLICT (id) DO UPDATE SET
		type = EXCLUDED.type, severity = EXCLUDED.severity, timestamp = EXCLUDED.timestamp,
		title = EXCLUDED.title, message = EXCLUDED.message, source = EXCLUDED.source,
		reference_id = EXCLUDED.reference_id, status = EXCLUDED.status
	RETURNING created_at, updated_at
	`
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	var createdAt, updatedAt time.Time
	// Use model.NullableString from the model package correctly
	err := db.QueryRowContext(execCtx, query,
		a.ID, a.Type, a.Severity, a.Timestamp, a.Title, a.Message,
		model.NullableString(a.Source),
		model.NullableString(a.ReferenceID),
		model.NullableString(a.Status),
	).Scan(&createdAt, &updatedAt)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("db query timeout saving alert ID %s: %w", a.ID, err)
		}
		return fmt.Errorf("db query failed for alert ID %s: %w", a.ID, err)
	}
	log.Printf("DEBUG: Alert ID %s saved/updated successfully. Created: %s, Updated: %s", a.ID, createdAt.Format(time.RFC3339), updatedAt.Format(time.RFC3339))
	return nil
}

// GetAlertByID retrieves a single alert by its unique ID.
func (db *DB) GetAlertByID(ctx context.Context, id string) (model.Alert, error) {
	query := `
	SELECT id, type, severity, timestamp, title, message, source, reference_id, status, created_at, updated_at
	FROM alerts WHERE id = $1
	`
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	row := db.QueryRowContext(queryCtx, query, id)
	var a model.Alert
	var source, referenceID, status sql.NullString
	var createdAt, updatedAt sql.NullTime
	err := row.Scan(&a.ID, &a.Type, &a.Severity, &a.Timestamp, &a.Title, &a.Message, &source, &referenceID, &status, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Alert{}, fmt.Errorf("alert with ID %s not found: %w", id, err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return model.Alert{}, fmt.Errorf("db query timeout getting alert ID %s: %w", id, err)
		}
		return model.Alert{}, fmt.Errorf("db query scan failed for alert ID %s: %w", id, err)
	}
	a.Source = source.String
	a.ReferenceID = referenceID.String
	a.Status = status.String
	if createdAt.Valid {
		a.CreatedAt = createdAt.Time.UTC()
	}
	if updatedAt.Valid {
		a.UpdatedAt = updatedAt.Time.UTC()
	}
	a.Timestamp = a.Timestamp.UTC()
	return a, nil
}

// Filter holds criteria for querying alerts.
type Filter struct {
	Types       []string
	MinSeverity *int
	MaxSeverity *int
	Status      *string
	Source      *string
	ReferenceID *string
	Since       *time.Time
	Until       *time.Time
	SortAsc     bool
	Limit       int
	Offset      int
}

// ListAlerts retrieves a list of alerts based on filter criteria.
func (db *DB) ListAlerts(ctx context.Context, f Filter) ([]model.Alert, error) {
	if f.Limit <= 0 {
		f.Limit = defaultListLimit
	} else if f.Limit > maxListLimit {
		f.Limit = maxListLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var queryBuilder strings.Builder
	queryBuilder.WriteString(`SELECT id, type, severity, timestamp, title, message, source, reference_id, status, created_at, updated_at FROM alerts`)
	conditions := []string{}
	args := []interface{}{}
	argID := 1

	if len(f.Types) > 0 {
		placeholders := make([]string, len(f.Types))
		for i, t := range f.Types {
			placeholders[i] = fmt.Sprintf("$%d", argID)
			args = append(args, t)
			argID++
		}
		conditions = append(conditions, fmt.Sprintf("type IN (%s)", strings.Join(placeholders, ",")))
	}
	if f.MinSeverity != nil {
		conditions = append(conditions, fmt.Sprintf("severity >= $%d", argID))
		args = append(args, *f.MinSeverity)
		argID++
	}
	if f.MaxSeverity != nil {
		conditions = append(conditions, fmt.Sprintf("severity <= $%d", argID))
		args = append(args, *f.MaxSeverity)
		argID++
	}
	if f.Status != nil {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argID))
		args = append(args, *f.Status)
		argID++
	}
	if f.Source != nil {
		conditions = append(conditions, fmt.Sprintf("source = $%d", argID))
		args = append(args, *f.Source)
		argID++
	}
	if f.ReferenceID != nil {
		conditions = append(conditions, fmt.Sprintf("reference_id = $%d", argID))
		args = append(args, *f.ReferenceID)
		argID++
	}
	if f.Since != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argID))
		args = append(args, f.Since.UTC())
		argID++
	}
	if f.Until != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argID))
		args = append(args, f.Until.UTC())
		argID++
	}

	if len(conditions) > 0 {
		queryBuilder.WriteString(" WHERE ")
		queryBuilder.WriteString(strings.Join(conditions, " AND "))
	}
	queryBuilder.WriteString(" ORDER BY timestamp ")
	if f.SortAsc {
		queryBuilder.WriteString("ASC")
	} else {
		queryBuilder.WriteString("DESC")
	}
	queryBuilder.WriteString(", created_at DESC")
	queryBuilder.WriteString(fmt.Sprintf(" LIMIT $%d", argID))
	args = append(args, f.Limit)
	argID++
	queryBuilder.WriteString(fmt.Sprintf(" OFFSET $%d", argID))
	args = append(args, f.Offset)
	argID++

	finalQuery := queryBuilder.String() // Use the built query
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	rows, err := db.QueryContext(queryCtx, finalQuery, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("db query timeout listing alerts: %w", err)
		}
		return nil, fmt.Errorf("db query failed listing alerts: %w", err)
	}
	defer rows.Close()

	list := make([]model.Alert, 0, f.Limit)
	for rows.Next() {
		var a model.Alert
		var source, referenceID, status sql.NullString
		var createdAt, updatedAt sql.NullTime
		if err := rows.Scan(&a.ID, &a.Type, &a.Severity, &a.Timestamp, &a.Title, &a.Message, &source, &referenceID, &status, &createdAt, &updatedAt); err != nil {
			log.Printf("ERROR: Failed to scan alert row: %v", err)
			continue
		}
		a.Source = source.String
		a.ReferenceID = referenceID.String
		a.Status = status.String
		if createdAt.Valid {
			a.CreatedAt = createdAt.Time.UTC()
		}
		if updatedAt.Valid {
			a.UpdatedAt = updatedAt.Time.UTC()
		}
		a.Timestamp = a.Timestamp.UTC()
		list = append(list, a)
	}
	if err = rows.Err(); err != nil {
		return list, fmt.Errorf("error iterating alert rows: %w", err)
	}
	if queryCtx.Err() != nil {
		log.Printf("WARNING: Context deadline/cancel exceeded after processing rows: %v", queryCtx.Err())
	}
	return list, nil
}

// Close wraps the underlying sql.DB Close method.
func (db *DB) Close() error {
	log.Println("INFO: Closing database connection pool...")
	return db.DB.Close()
}

// PingContext wraps the underlying sql.DB PingContext method.
func (db *DB) PingContext(ctx context.Context) error {
	return db.DB.PingContext(ctx)
}
