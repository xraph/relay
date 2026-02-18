package catalog

import "strings"

// Match checks if an event type name matches a subscription pattern.
//
// Supported patterns:
//
//	"invoice.created"  → exact match
//	"invoice.*"        → matches invoice.created, invoice.paid, etc. (single segment wildcard)
//	"*"                → matches everything
func Match(pattern, eventType string) bool {
	if pattern == "*" {
		return true
	}

	if pattern == eventType {
		return true
	}

	patternParts := strings.Split(pattern, ".")
	eventParts := strings.Split(eventType, ".")

	if len(patternParts) != len(eventParts) {
		return false
	}

	for i, pp := range patternParts {
		if pp == "*" {
			continue
		}
		if pp != eventParts[i] {
			return false
		}
	}

	return true
}
