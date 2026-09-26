package requestid

import "context"

type contextKey struct{}

// WithContext stores only a safe correlation ID in the request context.
func WithContext(ctx context.Context, id string) context.Context {
	if !Valid(id) {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns an ID only when it remains safe for a wire header.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	if !Valid(id) {
		return ""
	}
	return id
}

func Valid(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
