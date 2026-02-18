// Example: Stripe-style webhook signature verification.
//
// Demonstrates:
//   - How a webhook receiver verifies HMAC-SHA256 signatures
//   - Using the signature package to verify incoming webhooks
//   - Extracting timestamp and signature from headers
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/relay/signature"
)

const (
	// In production, retrieve this from the endpoint's secret.
	webhookSecret = "whsec_test_secret_for_demo"

	// Maximum age of a webhook signature (5 minutes).
	maxTimestampAge = 5 * time.Minute
)

func main() {
	http.HandleFunc("/webhook", handleWebhook)

	addr := ":9090"
	fmt.Println("Webhook receiver listening at http://localhost" + addr + "/webhook")
	fmt.Println()
	fmt.Println("Simulated delivery:")
	fmt.Println("  curl -X POST http://localhost:9090/webhook \\")
	fmt.Println("    -H 'Content-Type: application/json' \\")
	fmt.Println("    -H 'X-Relay-Signature: v1=<hmac>' \\")
	fmt.Println("    -H 'X-Relay-Timestamp: <unix>' \\")
	fmt.Println("    -d '{\"order_id\":\"ORD-001\"}'")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Read the request body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 2. Extract headers.
	sig := r.Header.Get("X-Relay-Signature")
	tsStr := r.Header.Get("X-Relay-Timestamp")
	eventID := r.Header.Get("X-Relay-Event-ID")

	if sig == "" || tsStr == "" {
		http.Error(w, "missing signature headers", http.StatusBadRequest)
		return
	}

	// 3. Parse and validate timestamp (prevent replay attacks).
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}

	eventTime := time.Unix(ts, 0)
	if time.Since(eventTime) > maxTimestampAge {
		http.Error(w, "timestamp too old", http.StatusBadRequest)
		return
	}

	// 4. Verify the HMAC-SHA256 signature.
	// The signature format is "v1=<hex>". Extract the versioned signatures.
	verified := false
	for _, s := range strings.Split(sig, ",") {
		s = strings.TrimSpace(s)
		if signature.Verify(body, webhookSecret, ts, s) {
			verified = true
			break
		}
	}

	if !verified {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	// 5. Process the webhook.
	fmt.Printf("Verified webhook: event_id=%s body=%s\n", eventID, string(body))
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}
