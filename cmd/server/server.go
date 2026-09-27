package main

import (
	"net/http"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
)

// newHTTPServer applies the configured timeouts to the billing HTTP server (KEL-71).
// Without them a client that stalls while sending headers or a body, or while reading a
// response, holds a goroutine and a file descriptor indefinitely; the Duitku webhook route
// is public. The write timeout is validated at configuration load to exceed the longest
// outbound wait a request can have, so a slow but healthy Duitku or Academic call is never
// cut short by the server itself.
func newHTTPServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              "0.0.0.0:" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: time.Duration(cfg.ServerReadHeaderTimeout) * time.Second,
		ReadTimeout:       time.Duration(cfg.ServerReadTimeout) * time.Second,
		WriteTimeout:      time.Duration(cfg.ServerWriteTimeout) * time.Second,
		IdleTimeout:       time.Duration(cfg.ServerIdleTimeout) * time.Second,
	}
}
