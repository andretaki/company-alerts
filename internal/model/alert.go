// internal/model/alert.go
package model

import (
	"database/sql" // Needed for sql.NullString
	"fmt"
	"strings"
	"time"
)

// Alert represents a structured notification within the company system.
type Alert struct {
	ID          string    `json:"id"`                     // Unique identifier (e.g., UUID)
	Type        string    `json:"type"`                   // Category (e.g., "complaint", "order_error", "system", "security")
	Severity    int       `json:"severity"`               // Numerical severity (e.g., 1=Low, 2=Medium, 3=High, 4=Critical)
	Timestamp   time.Time `json:"timestamp"`              // Time the event occurred (UTC)
	Title       string    `json:"title"`                  // Concise summary/headline
	Message     string    `json:"message"`                // Detailed description/message body
	Source      string    `json:"source,omitempty"`       // Optional: Originating system/service (e.g., "CRM", "OrderSystem", "Monitoring")
	ReferenceID string    `json:"reference_id,omitempty"` // Optional: Related ID (e.g., Order ID, Customer ID, Ticket #)
	Status      string    `json:"status,omitempty"`       // Optional: status (e.g., "new", "investigating", "resolved", "closed")
	CreatedAt   time.Time `json:"created_at,omitempty"`   // When the alert record was created in our system (DB only) - Optional in JSON
	UpdatedAt   time.Time `json:"updated_at,omitempty"`   // When the alert record was last updated (DB only) - Optional in JSON
}

// String provides a concise string representation of the alert.
func (a Alert) String() string {
	return fmt.Sprintf("[%s][Sev:%d] %s (%s)", strings.ToUpper(a.Type), a.Severity, a.Title, a.ID)
}

// Validate checks for essential fields and sets defaults.
func (a *Alert) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("alert ID is required")
	}
	a.Type = strings.TrimSpace(strings.ToLower(a.Type))
	if a.Type == "" {
		return fmt.Errorf("alert type is required")
	}
	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		return fmt.Errorf("alert title is required")
	}
	a.Message = strings.TrimSpace(a.Message)
	if a.Message == "" {
		return fmt.Errorf("alert message is required")
	}
	if a.Timestamp.IsZero() {
		a.Timestamp = time.Now().UTC() // Default timestamp if not provided
	} else {
		a.Timestamp = a.Timestamp.UTC() // Ensure UTC
	}
	if a.Severity <= 0 {
		a.Severity = 1 // Default severity
	}
	// Validate status if provided
	if a.Status != "" {
		a.Status = strings.TrimSpace(strings.ToLower(a.Status))
		if !isValidStatus(a.Status) {
			return fmt.Errorf("invalid alert status '%s'", a.Status)
		}
	}

	// Trim optional fields
	a.Source = strings.TrimSpace(a.Source)
	a.ReferenceID = strings.TrimSpace(a.ReferenceID)

	return nil
}

// isValidStatus checks if the status is one of the allowed values.
// Keep this internal or move to a constants package if needed elsewhere.
func isValidStatus(status string) bool {
	// Define allowed statuses (lowercase)
	validStatuses := map[string]struct{}{
		"new":           {},
		"acknowledged":  {},
		"investigating": {},
		"resolved":      {},
		"closed":        {},
		"ignored":       {}, // Added another example status
	}
	_, ok := validStatuses[status] // Check against lowercase map
	return ok
}

// NullableString converts a string to sql.NullString for database interaction.
// Handles empty strings correctly by setting Valid to false.
func NullableString(s string) sql.NullString {
	trimmed := strings.TrimSpace(s)
	return sql.NullString{
		String: trimmed,
		Valid:  trimmed != "", // Valid is true only if the trimmed string is not empty
	}
}
