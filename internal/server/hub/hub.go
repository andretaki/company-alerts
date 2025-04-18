package hub

import (
	"log"
	"sync"

	"github.com/yourorg/company-alerts/internal/model"
)

// Hub maintains the set of active clients and broadcasts messages to them.
type Hub struct {
	// Registered clients. Use map for easy add/remove. bool value is always true.
	clients map[*Client]bool

	// Inbound messages from the clients (not used in this simple broadcast model).
	// broadcast chan []byte

	// Channel for messages to be broadcast to all clients.
	Broadcast chan model.Alert

	// Register requests from the clients.
	Register chan *Client

	// Unregister requests from clients.
	Unregister chan *Client

	// Mutex to protect concurrent access to the clients map
	mu sync.RWMutex
}

// NewHub creates a new Hub instance.
func NewHub() *Hub {
	return &Hub{
		Broadcast:  make(chan model.Alert),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

// Run starts the hub's event loop in a goroutine.
func (h *Hub) Run() {
	log.Println("WebSocket Hub started")
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = true
			log.Printf("Client registered: %s (Total: %d)", client.Conn.RemoteAddr(), len(h.clients))
			h.mu.Unlock()

		case client := <-h.Unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send) // Close the client's send channel
				log.Printf("Client unregistered: %s (Total: %d)", client.Conn.RemoteAddr(), len(h.clients))
			}
			h.mu.Unlock()

		case alert := <-h.Broadcast:
			h.mu.RLock() // Use read lock for iteration
			log.Printf("Broadcasting alert ID: %s to %d clients", alert.ID, len(h.clients))
			for client := range h.clients {
				// Non-blocking send: if a client's buffer is full, skip them
				select {
				case client.Send <- alert:
				default:
					// Buffer full, client might be slow or disconnected
					log.Printf("Client send buffer full, closing & unregistering: %s", client.Conn.RemoteAddr())
					// Need to unregister outside the RLock
					go func(c *Client) { h.Unregister <- c }(client)
				}
			}
			h.mu.RUnlock() // Release read lock
		}
	}
}
