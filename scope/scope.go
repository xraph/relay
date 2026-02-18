// Package scope captures and restores multi-tenant scope from context.
// In standalone mode, scope is a no-op. When used with Forge, it extracts
// app and organization IDs from the context.
package scope

import "context"

// Capture extracts the current app and org scope from the context.
// Returns empty strings when not running inside Forge.
func Capture(_ context.Context) (appID, orgID string) {
	return "", ""
}

// Restore injects app and org scope into the context.
// Returns the context unchanged when scope values are empty.
func Restore(ctx context.Context, _, _ string) context.Context {
	return ctx
}
