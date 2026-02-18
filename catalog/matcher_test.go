package catalog

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern   string
		eventType string
		want      bool
	}{
		// Wildcard "*" matches everything.
		{"*", "invoice.created", true},
		{"*", "user.deleted", true},
		{"*", "x", true},

		// Exact match.
		{"invoice.created", "invoice.created", true},
		{"user.deleted", "user.deleted", true},

		// Exact mismatch.
		{"invoice.created", "invoice.paid", false},
		{"invoice.created", "user.created", false},

		// Single-segment wildcard.
		{"invoice.*", "invoice.created", true},
		{"invoice.*", "invoice.paid", true},
		{"invoice.*", "user.created", false},
		{"*.created", "invoice.created", true},
		{"*.created", "user.created", true},
		{"*.created", "invoice.paid", false},

		// Multi-segment with wildcard.
		{"invoice.*.completed", "invoice.payment.completed", true},
		{"invoice.*.completed", "invoice.payment.failed", false},
		{"*.payment.*", "invoice.payment.completed", true},
		{"*.payment.*", "invoice.refund.completed", false},

		// Segment count mismatch.
		{"invoice.*", "invoice.payment.completed", false},
		{"invoice.*.completed", "invoice.paid", false},
		{"invoice", "invoice.created", false},

		// Edge cases.
		{"", "", true},
		{"a", "a", true},
		{"a", "b", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_vs_"+tt.eventType, func(t *testing.T) {
			got := Match(tt.pattern, tt.eventType)
			if got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.eventType, got, tt.want)
			}
		})
	}
}
