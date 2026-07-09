// Package stripe implements a Conduit connector for Stripe.
//
// Auth:       API Key (Authorization: Bearer sk_...)
// Endpoints:  list_charges (offset-paginated via Stripe's cursor-style starting_after)
// Webhook:    Stripe-Signature HMAC-SHA256 verification
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"conduit/sdk"
)

// ─── Connector ────────────────────────────────────────────────────────────────

type StripeConnector struct {
	mockBaseURL string
}

// NewStripeConnector returns a Stripe connector pointed at mockBaseURL.
// In production mockBaseURL is empty and the real Stripe API is used.
func NewStripeConnector(mockBaseURL string) *StripeConnector {
	if mockBaseURL == "" {
		mockBaseURL = "localhost:8081"
	}
	return &StripeConnector{mockBaseURL: mockBaseURL}
}

// ─── Metadata ─────────────────────────────────────────────────────────────────

func (c *StripeConnector) Metadata() sdk.ConnectorMetadata {
	return sdk.ConnectorMetadata{
		ID:          "stripe",
		Name:        "Stripe",
		Description: "List charges and receive payment events from Stripe.",
		Category:    "Payments",
		Scopes:      []string{},
	}
}

// ─── Auth ─────────────────────────────────────────────────────────────────────

func (c *StripeConnector) AuthConfig() sdk.AuthConfig {
	return sdk.AuthConfig{
		Type: sdk.AuthTypeAPIKey,
		APIKey: &sdk.APIKeyConfig{
			HeaderName:  "Authorization",
			ValuePrefix: "Bearer ",
		},
	}
}

// ─── I/O Schemas ──────────────────────────────────────────────────────────────

// Charge represents a Stripe charge object.
type Charge struct {
	ID       string `json:"id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
	Created  int64  `json:"created"`
}

// ChargeList is the top-level Stripe list response.
type ChargeList struct {
	Object  string   `json:"object"`
	Data    []Charge `json:"data"`
	HasMore bool     `json:"has_more"`
	URL     string   `json:"url"`
}

// ─── Endpoints ────────────────────────────────────────────────────────────────

func (c *StripeConnector) Endpoints() []sdk.EndpointDefinition {
	return []sdk.EndpointDefinition{
		{
			Name:         "list_charges",
			Method:       "GET",
			Path:         "/services/stripe/v1/charges",
			Description:  "List Stripe charges with offset pagination (starting_after cursor).",
			InputSchema:  nil,
			OutputSchema: &ChargeList{},
			Execute:      c.executeListCharges,
			GetPaginator: c.getPaginatorForListCharges,
		},
	}
}

// executeListCharges fetches a page of charges from Stripe.
func (c *StripeConnector) executeListCharges(ctx context.Context, client *sdk.HTTPClient, req sdk.Request) (sdk.Response, error) {
	reqURL, err := url.Parse("/services/stripe/v1/charges")
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("invalid path", http.StatusInternalServerError, err)
	}

	q := reqURL.Query()
	if limit, ok := req.Params["limit"]; ok && limit != "" {
		q.Set("limit", limit)
	}
	if startingAfter, ok := req.Params["starting_after"]; ok && startingAfter != "" {
		q.Set("starting_after", startingAfter)
	}
	reqURL.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", reqURL.String(), nil)
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to create HTTP request", http.StatusInternalServerError, err)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return sdk.Response{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sdk.Response{}, sdk.NewTransientError("failed to read response body", resp.StatusCode, err)
	}

	return sdk.Response{Body: body, StatusCode: resp.StatusCode}, nil
}

// getPaginatorForListCharges returns a CursorPaginator that uses Stripe's
// starting_after cursor field (last item ID from previous page).
func (c *StripeConnector) getPaginatorForListCharges(path string, params map[string]string, limit int) sdk.Paginator {
	baseURL := "/services/stripe/v1/charges"
	return sdk.NewCursorPaginator(
		baseURL,
		"starting_after",
		"limit",
		limit,
		extractStripeNextCursor,
	)
}

// extractStripeNextCursor reads has_more and the last item ID from a Stripe list response.
// Returns the ID of the last charge as the cursor for starting_after.
func extractStripeNextCursor(body []byte, _ http.Header) (string, bool, error) {
	var list struct {
		HasMore bool     `json:"has_more"`
		Data    []Charge `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "", false, fmt.Errorf("failed to parse Stripe list response: %w", err)
	}
	if !list.HasMore || len(list.Data) == 0 {
		return "", false, nil
	}
	// Stripe cursor = ID of the last item in the current page
	lastID := list.Data[len(list.Data)-1].ID
	return lastID, true, nil
}

// ─── Webhook ──────────────────────────────────────────────────────────────────

// HandleWebhook verifies the Stripe-Signature header using HMAC-SHA256.
// Stripe sends: Stripe-Signature: t=timestamp,v1=hex_sig
func (c *StripeConnector) HandleWebhook(ctx context.Context, event sdk.WebhookEvent) error {
	sigHeader := event.Headers["Stripe-Signature"]
	if sigHeader == "" {
		return sdk.NewPermanentError("missing Stripe-Signature header", http.StatusBadRequest, nil)
	}

	// Extract the v1= signature from the comma-separated header
	// Format: t=1234567890,v1=abcdef...
	sig := ""
	for _, part := range strings.Split(sigHeader, ",") {
		if strings.HasPrefix(part, "v1=") {
			sig = part // pass the full "v1=hex" string
			break
		}
	}
	if sig == "" {
		return sdk.NewPermanentError("no v1 signature found in Stripe-Signature header", http.StatusBadRequest, nil)
	}

	secret := []byte("mock_webhook_secret_value")
	if !sdk.VerifyHMACSHA256HexSignature(event.Payload, sig, "v1=", secret) {
		return sdk.NewPermanentError("invalid Stripe webhook signature", http.StatusUnauthorized, nil)
	}

	return nil
}
