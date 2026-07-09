package gateway

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"conduit/sdk"
)

func (g *Gateway) handleWebhookPayload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// URL shape: /webhooks/{instance_id}
	instanceID := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	if instanceID == "" || strings.Contains(instanceID, "/") {
		writeJSONError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	// Load instance
	inst, err := g.db.GetInstance(instanceID)
	if err != nil {
		RecordWebhookFailure("unknown", "instance_not_found")
		writeJSONError(w, http.StatusNotFound, "Instance not found")
		return
	}

	// Resolve connector
	conn, ok := g.registry[inst.ConnectorID]
	if !ok {
		RecordWebhookFailure(inst.ConnectorID, "connector_not_found")
		writeJSONError(w, http.StatusInternalServerError, "Connector not registered")
		return
	}

	// Read raw body payload
	payloadBytes, err := io.ReadAll(r.Body)
	if err != nil {
		RecordWebhookFailure(inst.ConnectorID, "cannot_read_body")
		writeJSONError(w, http.StatusBadRequest, "Cannot read request body")
		return
	}

	// Copy headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	// Build WebhookEvent
	event := sdk.WebhookEvent{
		Payload:   payloadBytes,
		Headers:   headers,
		EventType: "webhook", // Can parse more granularly per provider if needed
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Handle Webhook (this verifies HMAC signature inside the connector code!)
	err = conn.HandleWebhook(ctx, event)
	if err != nil {
		log.Printf("[Gateway] Webhook verification failed for instance %s: %v", instanceID, err)
		
		reason := "verification_failed"
		if strings.Contains(err.Error(), "signature") {
			reason = "signature_mismatch"
		}
		RecordWebhookFailure(inst.ConnectorID, reason)

		writeJSONError(w, http.StatusUnauthorized, "Webhook verification failed: "+err.Error())
		return
	}

	log.Printf("[Gateway] Webhook delivered successfully for instance %s (%s)", instanceID, inst.ConnectorID)

	// Return success
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"received"}`))
}
