package stripe_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"conduit/connectors/stripe"
	"conduit/mock"
	"conduit/sdk"
	"context"
)

const webhookSecret = "mock_webhook_secret_value"

func newTestServer() (*mock.MockServer, *httptest.Server, string) {
	ms := mock.NewMockServer("", webhookSecret)
	ts := httptest.NewServer(ms.Handler())
	host := ts.Listener.Addr().String()
	return ms, ts, host
}

func TestStripeMetadata(t *testing.T) {
	conn := stripe.NewStripeConnector("")
	meta := conn.Metadata()
	if meta.ID != "stripe" {
		t.Errorf("want id=stripe, got %q", meta.ID)
	}
	if meta.Name != "Stripe" {
		t.Errorf("want name=Stripe, got %q", meta.Name)
	}
	if conn.AuthConfig().Type != sdk.AuthTypeAPIKey {
		t.Error("want APIKey auth type")
	}
}

func TestStripeListCharges(t *testing.T) {
	_, ts, host := newTestServer()
	defer ts.Close()

	conn := stripe.NewStripeConnector(host)
	creds := sdk.Credentials{APIKey: "sk_test_mock_key_12345"}
	client := sdk.NewHTTPClient("http://"+host, creds, conn.AuthConfig(), nil)

	var listEP *sdk.EndpointDefinition
	for _, ep := range conn.Endpoints() {
		if ep.Name == "list_charges" {
			epCopy := ep
			listEP = &epCopy
			break
		}
	}
	if listEP == nil {
		t.Fatal("list_charges endpoint not found")
	}

	resp, err := listEP.Execute(context.Background(), client, sdk.Request{
		Params: map[string]string{"limit": "3"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	var list struct {
		Object  string        `json:"object"`
		Data    []interface{} `json:"data"`
		HasMore bool          `json:"has_more"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Object != "list" {
		t.Errorf("want object=list, got %q", list.Object)
	}
	if len(list.Data) == 0 {
		t.Error("expected at least one charge in response")
	}
	if !list.HasMore {
		t.Error("expected has_more=true for first page")
	}
}

func TestStripeListCharges_Pagination(t *testing.T) {
	_, ts, host := newTestServer()
	defer ts.Close()

	conn := stripe.NewStripeConnector(host)
	creds := sdk.Credentials{APIKey: "sk_test_mock_key_12345"}
	client := sdk.NewHTTPClient("http://"+host, creds, conn.AuthConfig(), nil)

	var listEP *sdk.EndpointDefinition
	for _, ep := range conn.Endpoints() {
		if ep.Name == "list_charges" {
			epCopy := ep
			listEP = &epCopy
			break
		}
	}

	// Second page — pass starting_after
	resp, err := listEP.Execute(context.Background(), client, sdk.Request{
		Params: map[string]string{"starting_after": "ch_page1_3", "limit": "3"},
	})
	if err != nil {
		t.Fatalf("page 2 execute: %v", err)
	}

	var list struct {
		HasMore bool          `json:"has_more"`
		Data    []interface{} `json:"data"`
	}
	json.Unmarshal(resp.Body, &list)

	if list.HasMore {
		t.Error("expected has_more=false on last page")
	}
	if len(list.Data) == 0 {
		t.Error("expected items on page 2")
	}
}

func TestStripeListCharges_Unauthorized(t *testing.T) {
	_, ts, host := newTestServer()
	defer ts.Close()

	conn := stripe.NewStripeConnector(host)
	// Wrong key format (no sk_ prefix)
	creds := sdk.Credentials{APIKey: "wrong_key"}
	client := sdk.NewHTTPClient("http://"+host, creds, conn.AuthConfig(), nil)

	var listEP *sdk.EndpointDefinition
	for _, ep := range conn.Endpoints() {
		if ep.Name == "list_charges" {
			epCopy := ep
			listEP = &epCopy
			break
		}
	}

	_, err := listEP.Execute(context.Background(), client, sdk.Request{})
	if err == nil {
		t.Fatal("expected error for invalid API key")
	}
}

func TestStripeWebhookValid(t *testing.T) {
	conn := stripe.NewStripeConnector("")
	payload := []byte(`{"type":"charge.succeeded","id":"ch_test_001"}`)

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	sig := "v1=" + hex.EncodeToString(mac.Sum(nil))

	event := sdk.WebhookEvent{
		Payload:   payload,
		EventType: "charge.succeeded",
		Headers: map[string]string{
			"Stripe-Signature": "t=1234567890," + sig,
		},
	}

	if err := conn.HandleWebhook(context.Background(), event); err != nil {
		t.Fatalf("expected valid webhook to pass, got: %v", err)
	}
}

func TestStripeWebhookInvalidSignature(t *testing.T) {
	conn := stripe.NewStripeConnector("")
	payload := []byte(`{"type":"charge.succeeded"}`)

	event := sdk.WebhookEvent{
		Payload:   payload,
		EventType: "charge.succeeded",
		Headers: map[string]string{
			"Stripe-Signature": "t=1234567890,v1=badbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadb",
		},
	}

	if err := conn.HandleWebhook(context.Background(), event); err == nil {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestStripeWebhookMissingHeader(t *testing.T) {
	conn := stripe.NewStripeConnector("")
	event := sdk.WebhookEvent{
		Payload:  []byte(`{}`),
		Headers:  map[string]string{},
	}
	if err := conn.HandleWebhook(context.Background(), event); err == nil {
		t.Fatal("expected missing header to fail")
	}
}

func TestStripePaginatorCursorExtract(t *testing.T) {
	// Verify the paginator signals continuation correctly
	_, ts, host := newTestServer()
	defer ts.Close()

	conn := stripe.NewStripeConnector(host)
	paginator := conn.Endpoints()[0].GetPaginator("/services/stripe/v1/charges", nil, 3)
	if paginator == nil {
		t.Fatal("paginator should not be nil")
	}
	if !paginator.HasNext() {
		t.Fatal("paginator should have next on first check")
	}

	creds := sdk.Credentials{APIKey: "sk_test_mock_key_12345"}
	client := sdk.NewHTTPClient("http://"+host, creds, conn.AuthConfig(), nil)

	// Fetch page 1
	page1, err := paginator.Next(context.Background(), client)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}

	var list1 struct {
		HasMore bool          `json:"has_more"`
		Data    []interface{} `json:"data"`
	}
	json.Unmarshal(page1, &list1)
	if len(list1.Data) == 0 {
		t.Fatal("expected data on page 1")
	}

	// Paginator should still have next
	if !paginator.HasNext() {
		t.Error("expected paginator to have more pages after page 1")
	}
}

// Ensure the Stripe connector compiles and bytes.Buffer import is used
var _ = bytes.NewBuffer
