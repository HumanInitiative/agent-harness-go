// Package caller carries who made a request through a context, so code
// far from the HTTP layer (a tool, for instance) can authorize an action
// without importing the adapter that authenticated the caller.
package caller

import "context"

type keyIDKey struct{}

// WithKeyID records the fingerprint of the API key that authenticated the
// request.
func WithKeyID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyIDKey{}, id)
}

// KeyID returns the fingerprint of the API key that authenticated the
// request, or "" when the request was not authenticated by one.
func KeyID(ctx context.Context) string {
	id, _ := ctx.Value(keyIDKey{}).(string)
	return id
}
