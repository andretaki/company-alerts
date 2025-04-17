// /home/andre/company-alerts/go.mod
module github.com/yourorg/company-alerts

go 1.22

require (
	github.com/getlantern/systray v0.2.2
	github.com/go-toast/toast v1.0.0
	github.com/gorilla/websocket v1.5.1
	github.com/lib/pq v1.10.9
)

// Add replace directives if using local forks, otherwise leave empty.
// Example: replace github.com/yourorg/some-dep => ../some-dep