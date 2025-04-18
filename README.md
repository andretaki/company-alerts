# Company Alerts Server

A simple server for broadcasting alerts to connected clients via WebSockets.

## Features

- WebSocket server for real-time alert delivery
- REST API for creating new alerts
- Authentication via tokens
- In-memory storage of recent alerts

## Setup

1. Install Go (1.20 or later)
2. Clone this repository
3. Set up environment variables (see `.env.example`)
4. Run the server:

```bash
# From project root
go run cmd/server/main.go
```

## Usage

### WebSocket Connection

Connect to the WebSocket endpoint with a valid token:

```
ws://localhost:8080/ws?token=your-token-here
```

### REST API

#### Create Alert

```
POST /api/alerts
Content-Type: application/json
X-API-Token: your-token-here

{
  "title": "Alert Title",
  "message": "Alert message details",
  "level": "info"  // info, warning, error, critical
}
```

#### List Alerts

```
GET /api/alerts
X-API-Token: your-token-here
```

## Configuration

Configuration is done through environment variables:

- `PORT`: Server port (default: 8080)
- `ALLOWED_TOKENS`: Comma-separated list of valid authentication tokens
- `ADD_TEST_DATA`: Set to "true" to add test alerts on startup
- `STATIC_DIR`: Directory for static files (default: ./static)

## Project Structure

```
company-alerts/
├── cmd/                 # Application entry points
│   └── server/          # Server executable
├── internal/            # Private application code
│   ├── model/           # Shared data models
│   └── server/          # Server implementation
│       ├── config/      # Server configuration
│       ├── handlers/    # HTTP handlers
│       └── hub/         # WebSocket hub
├── static/              # Static files (if needed)
├── go.mod               # Go module definition
├── go.sum               # Go module checksums
└── .env                 # Environment configuration
``` 