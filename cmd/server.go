package main

import (
	"minimalpanel/internal/conf"
	"minimalpanel/internal/netx"
	"minimalpanel/internal/web"
	"net/http"
)

func main() {
	conf.LoadConfig("config.toml")

	// Initialize the global Socket.IO server with all namespaces
	netx.SetupGlobalServer()

	// Setup Socket.IO services (they will use the global server)
	web.SetupSSHService()
	web.SetupDashboardService()

	// Register the Socket.IO handler once
	http.Handle("/socket.io/", netx.GetHandler())

	// Setup HTTP routes
	web.StartPages(http.DefaultServeMux)
	web.StartAssets(http.DefaultServeMux)
	web.StartIndex(http.DefaultServeMux)
	web.StartLogin(http.DefaultServeMux)
	web.StartSessionUtil(http.DefaultServeMux)

	http.ListenAndServe(":8080", nil)
}
