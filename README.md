# Company Alerts System

A real-time system for broadcasting and managing internal company alerts (e.g., customer complaints, order errors, system notifications) via WebSockets and a REST API, with a desktop client for notifications.

## Features

*   **Real-time Alerts:** WebSocket server pushes alerts instantly to connected clients.
*   **REST API:** Create, list, and retrieve alerts. Supports filtering and sorting.
*   **Persistent Storage:** Alerts are stored in a PostgreSQL database with automatic timestamp updates.
*   **Authentication:** Server authenticates API calls (`Authorization: Bearer <token>`) and WebSocket connections (`?token=<token>`).
*   **Desktop Client:** Cross-platform client displays toast notifications and provides a system tray icon for viewing history and quitting.
*   **Client-Side Filtering:** Client can be configured via environment variables to only show notifications for specific alert types or severities.
*   **Graceful Shutdown:** Server and client handle termination signals (SIGINT, SIGTERM) for clean shutdown.
*   **Configuration:** Both server and client are configured primarily via environment variables (see `.env.example`).

## Architecture

*   **Server (`cmd/alerts-server`):** Go application acting as the central hub.
    *   Handles WebSocket connections (`/ws`).
    *   Provides a REST API (`/api/alerts/`).
    *   Connects to PostgreSQL (`internal/storage`).
    *   Manages authentication and broadcasting (`internal/server`).
*   **Client (`cmd/alerts-client`):** Go application running on user desktops.
    *   Connects to the server via WebSocket or HTTP Polling (`internal/network`).
    *   Displays system tray icon and toast notifications (`internal/notifier`).
    *   Configurable via environment variables (`internal/config`).
*   **Shared Model (`internal/model`):** Defines the standard `Alert` structure used throughout the system.

## Alert Model (`internal/model/Alert`)

```json
// Example Alert structure
{
  "id": "unique-uuid-string",             // Required (or generated by server on POST)
  "type": "complaint",                    // Required (lowercase, e.g., complaint, order_error, system, security)
  "severity": 2,                          // Required (e.g., 1=Low, 2=Medium, 3=High, 4=Critical)
  "timestamp": "2023-10-27T10:30:00Z",    // Required (ISO 8601 UTC, defaults to now() if omitted on POST)
  "title": "Short Alert Headline",          // Required
  "message": "Detailed alert message body.",// Required
  "source": "CRM",                        // Optional: Originating system/service
  "reference_id": "C1234",                // Optional: Related ID (Order, Customer, Ticket)
  "status": "new",                        // Optional: e.g., new, investigating, resolved, closed (lowercase)
  "created_at": "2023-10-27T10:30:05Z",    // Read-only (set by DB)
  "updated_at": "2023-10-27T10:35:10Z"     // Read-only (set by DB trigger)
}