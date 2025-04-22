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
	CheckOrigin: func(r *http.Request) bool {
		// TODO: Implement proper origin checking for production.
		// Example: Check r.Header.Get("Origin") against a list of allowed domains.
		// allowedOrigins := []string{"http://localhost:3000", "https://yourfrontend.com"}
		// origin := r.Header.Get("Origin")
		// for _, allowed := range allowedOrigins {
		//     if origin == allowed {
		//         return true
		//     }
		// }
		// log.Printf("WARN: WebSocket connection rejected due to invalid origin: %s", origin)
		// return false
		return true // Allow all origins for now (development)
	},
	Error: func(w http.ResponseWriter, r *http.Request, status int, reason error) {
		// Log WebSocket upgrade errors
		log.Printf("ERROR: WebSocket upgrade failed for %s: status=%d, reason=%v", r.RemoteAddr, status, reason)
		// Default behavior is to write an HTTP error response, which is fine.
		http.Error(w, reason.Error(), status)
	},
}

// ServeWs handles websocket requests from clients.
func ServeWs(srvCfg *config.ServerConfig, h *hub.Hub, w http.ResponseWriter, r *http.Request) {
	// 1. Authentication (via query parameter for WebSocket)
	token := r.URL.Query().Get("token")
	if token == "" {
		log.Println("WARN: WebSocket connection attempt rejected: missing token query parameter")
		http.Error(w, "Missing token", http.StatusUnauthorized) // 401 Unauthorized
		return
	}
	if !srvCfg.IsTokenAllowed(token) {
		log.Printf("WARN: WebSocket connection attempt rejected: invalid token provided (prefix: %s...) for %s", token[:min(len(token), 4)], r.RemoteAddr)
		http.Error(w, "Invalid token", http.StatusForbidden) // 403 Forbidden for invalid token
		return
	}
	// Optionally log successful auth, maybe less verbosely
	// log.Printf("INFO: WebSocket token accepted for client: %s", r.RemoteAddr)

	// 2. Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil) // Pass nil response header
	if err != nil {
		// Error logged by the upgrader's Error function.
		// Upgrader automatically sends appropriate HTTP error response.
		return
	}
	log.Printf("INFO: WebSocket connection successfully upgraded for: %s", r.RemoteAddr)

	// 3. Create a new client instance
	client := &hub.Client{
		Hub:  h,
		Conn: conn,
		Send: make(chan model.Alert, 256), // Buffered channel for outgoing alerts to this client
	}

	// 4. Register the client with the hub
	client.Hub.Register <- client

	// 5. Start the client's read and write pumps in separate goroutines.
	// This allows the HTTP handler to return immediately, while the pumps
	// handle the lifecycle of the WebSocket connection.
	go client.WritePump()
	go client.ReadPump()

	// The handler function returns; the goroutines manage the connection.
}

// Helper for logging token prefix (already defined in alerts.go, keep consistent)
// func min(a, b int) int {
// 	if a < b { return a }
// 	return b
// }
