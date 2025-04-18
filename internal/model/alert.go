package model

import (
	"fmt"
	"time"
)

// Alert defines the structure of an incoming alert.
// This is now the central definition used across packages.
type Alert struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Severity  int    `json:"severity"`
	Timestamp int64  `json:"timestamp"` // Unix Milliseconds UTC
	Summary   string `json:"summary"`
	Detail    string `json:"detail"`
}

// Time returns the timestamp as a time.Time object.
func (a Alert) Time() time.Time {
	return time.UnixMilli(a.Timestamp)
}

// String provides a concise string representation of the alert.
func (a Alert) String() string {
	return fmt.Sprintf("[%s][Sev:%d] %s", a.Type, a.Severity, a.Summary)
}
