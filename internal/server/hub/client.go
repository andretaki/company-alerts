package hub

import (
	"bytes"
	"log"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yourorg/company-alerts/internal/model"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512 // Adjust as needed
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	Hub *Hub

	// The websocket connection.
	Conn *websocket.Conn

	// Buffered channel of outbound messages (Alerts).
	Send chan model.Alert // Changed to model.Alert
}

// ReadPump pumps messages from the websocket connection to the hub.
//
// The application runs ReadPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) ReadPump() {
	defer func() {
		c.Hub.Unregister <- c
		c.Conn.Close()
		log.Printf("Closed ReadPump for client: %s", c.Conn.RemoteAddr())
	}()
	c.Conn.SetReadLimit(maxMessageSize)
	// Set initial read deadline
	if err := c.Conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("Error setting read deadline for %s: %v", c.Conn.RemoteAddr(), err)
		// Don't return immediately, maybe it still works
	}
	c.Conn.SetPongHandler(func(string) error {
		// log.Printf("Pong received from %s", c.Conn.RemoteAddr()) // Debug
		// Reset read deadline upon receiving a pong
		if err := c.Conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			log.Printf("Error resetting read deadline for %s after pong: %v", c.Conn.RemoteAddr(), err)
			// If we can't reset deadline, maybe close connection? Or just log? Log for now.
		}
		return nil
	})

	// Loop reading messages (we don't expect any from client in this app, only control frames)
	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Unexpected WebSocket close error for %s: %v", c.Conn.RemoteAddr(), err)
			} else {
				log.Printf("WebSocket read error for %s (likely clean close): %v", c.Conn.RemoteAddr(), err)
			}
			break // Exit loop on any error
		}
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		// Log received message if needed for debugging, but typically ignore
		log.Printf("Received unexpected message from %s: %s", c.Conn.RemoteAddr(), message)
		// c.Hub.broadcast <- message // Example if echoing messages
	}
}

// WritePump pumps messages from the hub to the websocket connection.
//
// A goroutine running WritePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close() // Ensure connection is closed if WritePump exits
		log.Printf("Closed WritePump for client: %s", c.Conn.RemoteAddr())
	}()
	for {
		select {
		case alert, ok := <-c.Send: // Changed to receive model.Alert
			// Set write deadline for sending the actual message
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait)) // Error ignored, best effort
			if !ok {
				// The hub closed the channel.
				log.Printf("Hub closed Send channel for %s", c.Conn.RemoteAddr())
				_ = c.Conn.WriteMessage(websocket.CloseMessage, []byte{}) // Error ignored
				return
			}

			// Send the Alert struct as JSON
			err := c.Conn.WriteJSON(alert) // Use WriteJSON
			if err != nil {
				log.Printf("Error writing JSON to WebSocket for %s: %v", c.Conn.RemoteAddr(), err)
				// Don't necessarily return immediately, let ReadPump handle closure detection
				// But if write fails consistently, connection is likely dead.
				// Consider adding logic to trigger unregister if write errors persist.
				// For now, just log and continue the loop.
			} else {
				// log.Printf("Sent alert %s to %s", alert.ID, c.Conn.RemoteAddr()) // Debug
			}

		case <-ticker.C:
			// Send ping message
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait)) // Error ignored
			// log.Printf("Sending ping to %s", c.Conn.RemoteAddr()) // Debug
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("Error sending ping to %s: %v", c.Conn.RemoteAddr(), err)
				return // Assume connection is dead if ping fails
			}
		}
	}
}
