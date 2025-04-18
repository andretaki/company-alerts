package handlers

import (
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/yourorg/company-alerts/internal/model"
	"github.com/yourorg/company-alerts/internal/server/config"
	"github.com/yourorg/company-alerts/internal/server/hub"
)

// Configure the upgrader
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// CheckOrigin prevents CSRF attacks. Adjust for your needs.
	// Allow requests from any origin for simplicity in dev, OR specific origins.
	CheckOrigin: func(r *http.Request) bool {
		// Allow all for now - **REVIEW THIS FOR PRODUCTION**
		// You might want to check r.Header.Get("Origin") against a list of allowed domains.
		return true
	},
}

// ServeWs handles websocket requests from clients.
func ServeWs(srvCfg *config.ServerConfig, h *hub.Hub, w http.ResponseWriter, r *http.Request) {
	// 1. Authentication
	token := r.URL.Query().Get("token")
	if token == "" {
		log.Println("WebSocket connection attempt rejected: missing token")
		http.Error(w, "Missing token", http.StatusUnauthorized)
		return
	}
	if !srvCfg.IsTokenAllowed(token) {
		log.Printf("WebSocket connection attempt rejected: invalid token provided (prefix: %s...)", token[:min(len(token), 4)])
		http.Error(w, "Invalid token", http.StatusForbidden) // Use Forbidden instead of Unauthorized
		return
	}
	log.Printf("WebSocket token accepted for client: %s", r.RemoteAddr)

	// 2. Upgrade connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket connection for %s: %v", r.RemoteAddr, err)
		// Upgrader writes HTTP error response automatically
		return
	}
	log.Printf("WebSocket connection upgraded for: %s", r.RemoteAddr)

	// 3. Create and register client
	client := &hub.Client{
		Hub:  h,
		Conn: conn,
		Send: make(chan model.Alert, 256), // Create buffered channel for client
	}
	client.Hub.Register <- client

	// 4. Start reader/writer goroutines
	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.WritePump()
	go client.ReadPump()

	// The HTTP handler returns now, the goroutines handle the connection lifecycle.
}

// Helper for logging token prefix
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
