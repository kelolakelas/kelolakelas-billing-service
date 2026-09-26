package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/metadata"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/requestid"
)

const requestIDHeader = "X-Request-ID"

// RequestID returns the validated correlation ID, or empty for non-HTTP work.
func RequestID(ctx context.Context) string {
	return requestid.FromContext(ctx)
}

// OutgoingContext forwards only the validated ID, never inbound credentials.
func OutgoingContext(ctx context.Context) context.Context {
	if id := RequestID(ctx); id != "" {
		return metadata.AppendToOutgoingContext(ctx, "x-request-id", id)
	}
	return ctx
}

func validRequestID(value string) bool {
	return requestid.Valid(value)
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// RequestLog correlates access without logging headers, query strings, payloads or path parameters.
func RequestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(requestIDHeader))
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Request.Header.Set(requestIDHeader, id)
		c.Header(requestIDHeader, id)
		c.Request = c.Request.WithContext(requestid.WithContext(c.Request.Context(), id))
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		slog.InfoContext(c.Request.Context(), "http request", "request_id", id, "method", c.Request.Method, "route", path, "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
	}
}
