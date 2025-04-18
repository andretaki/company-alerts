package model

import (
	"fmt"
	"time"
)

// Alert represents an alert notification that can be sent to clients
type Alert struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Level     string    `json:"level"` // info, warning, error, critical
	Timestamp time.Time `json:"timestamp"`
}

// Time returns the timestamp as a time.Time object.
func (a Alert) Time() time.Time {
	return a.Timestamp
}

// String provides a concise string representation of the alert.
func (a Alert) String() string {
	return fmt.Sprintf("[%s] %s", a.Level, a.Message)
}

// Time returns the current time
func Time() time.Time {
	return time.Now()
}
