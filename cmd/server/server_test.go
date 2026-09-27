package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
)

func testServerConfig() config.Config {
	return config.Config{
		Port:                    "0",
		ServerReadHeaderTimeout: 1,
		ServerReadTimeout:       config.DefaultServerReadTimeout,
		ServerWriteTimeout:      config.DefaultServerWriteTimeout,
		ServerIdleTimeout:       config.DefaultServerIdleTimeout,
	}
}

// startServer serves the billing server built by newHTTPServer on a loopback port.
func startServer(t *testing.T, cfg config.Config) string {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	server := newHTTPServer(cfg, handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve returned %v", err)
		}
	})
	return listener.Addr().String()
}

func TestNewHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	cfg := config.Config{
		Port:                    "8082",
		ServerReadHeaderTimeout: 2,
		ServerReadTimeout:       15,
		ServerWriteTimeout:      45,
		ServerIdleTimeout:       90,
	}
	server := newHTTPServer(cfg, http.NotFoundHandler())
	if server.Addr != "0.0.0.0:8082" {
		t.Fatalf("Addr = %q", server.Addr)
	}
	if server.ReadHeaderTimeout != 2*time.Second || server.ReadTimeout != 15*time.Second ||
		server.WriteTimeout != 45*time.Second || server.IdleTimeout != 90*time.Second {
		t.Fatalf("timeouts = %s/%s/%s/%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

// KEL-71 acceptance: a client that never finishes its headers is disconnected once
// ReadHeaderTimeout elapses, instead of holding the connection open.
func TestSlowHeaderClientIsDisconnectedAfterReadHeaderTimeout(t *testing.T) {
	addr := startServer(t, testServerConfig())
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Headers are never terminated by the blank line.
	if _, err := io.WriteString(conn, "POST /api/v1/webhooks/duitku HTTP/1.1\r\nHost: billing\r\n"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// The client-side deadline only guards the test; the server must close first.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(conn)
	elapsed := time.Since(started)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("server kept the slow-header connection open for %s", elapsed)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("connection closed after %s, before the 1s ReadHeaderTimeout", elapsed)
	}
}

// Regression: a client that sends complete headers promptly is still served.
func TestPromptClientIsServedWithReadHeaderTimeout(t *testing.T) {
	addr := startServer(t, testServerConfig())
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /health HTTP/1.1\r\nHost: billing\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
}
