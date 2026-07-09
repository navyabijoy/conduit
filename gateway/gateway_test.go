package gateway

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"conduit/mock"
	"conduit/sdk"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newTestGateway stands up a real MockServer and a Gateway pointing at it,
// all in-process, using an in-memory SQLite database.
func newTestGateway(t *testing.T) (*Gateway, *mock.MockServer, func()) {
	t.Helper()

	const webhookSecret = "test_webhook_secret_value"

	// Start mock HTTP provider
	mockSrv := mock.NewMockServer("", webhookSecret)
	mockTS := httptest.NewServer(mockSrv.Handler())

	// Derive host:port from httptest URL (strip scheme)
	mockHost := strings.TrimPrefix(mockTS.URL, "http://")

	// Open in-memory SQLite database
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}

	gw := NewGateway(db, mockHost, webhookSecret)

	return gw, mockSrv, func() {
		mockTS.Close()
		db.Close()
	}
}

// gatewayServer wraps the Gateway in an httptest.Server so we can make real
// HTTP calls against it.
func gatewayServer(t *testing.T, gw *Gateway) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/connectors", gw.handleListConnectors)
	mux.HandleFunc("/v1/connectors/", gw.handleInstallConnector)
	mux.HandleFunc("/oauth/callback", gw.handleOAuthCallback)
	mux.HandleFunc("/v1/instances", gw.handleListInstances)
	mux.HandleFunc("/v1/instances/", gw.handleInstanceRoute)
	mux.HandleFunc("/webhooks/", gw.handleWebhookPayload)
	mux.HandleFunc("/openapi.json", gw.handleOpenAPISpec)
	mux.HandleFunc("/docs", gw.handleDocs)
	mux.HandleFunc("/docs/", gw.handleDocs)

	return httptest.NewServer(mux)
}

// ─── Tests: DB ────────────────────────────────────────────────────────────────

func TestDB_EncryptDecryptRoundTrip(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	plaintext := "super-secret-access-token-12345"
	encrypted, err := db.encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypted == plaintext {
		t.Fatal("encrypted value should not equal plaintext")
	}

	decrypted, err := db.decrypt(encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("want %q, got %q", plaintext, decrypted)
	}
}

func TestDB_EncryptEmptyString(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	enc, err := db.encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc != "" {
		t.Fatal("encrypting empty string should return empty string")
	}
}

func TestDB_SaveAndGetInstance(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	inst := &ConnectorInstance{
		ID:            "inst_test_001",
		ConnectorID:   "slack",
		Status:        "active",
		BaselineSchema: `{"ok":"boolean"}`,
		WebhookSecret: "secret",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
	}

	if err := db.SaveInstance(inst); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	got, err := db.GetInstance(inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.ID != inst.ID || got.ConnectorID != inst.ConnectorID || got.Status != inst.Status {
		t.Fatalf("mismatch: got %+v", got)
	}
}

func TestDB_SaveAndGetCredentials_OAuth2(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	// We need an instance first (FK constraint)
	inst := &ConnectorInstance{
		ID: "inst_creds_001", ConnectorID: "slack", Status: "active", CreatedAt: time.Now(),
	}
	db.SaveInstance(inst)

	creds := &sdk.Credentials{
		Token: &sdk.Token{
			AccessToken:  "access_abc",
			RefreshToken: "refresh_xyz",
			Expiry:       time.Now().Add(1 * time.Hour),
		},
	}

	if err := db.SaveCredentials("inst_creds_001", sdk.AuthTypeOAuth2, creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	got, authType, err := db.GetCredentials("inst_creds_001")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if authType != sdk.AuthTypeOAuth2 {
		t.Fatalf("want OAuth2, got %s", authType)
	}
	if got.Token.AccessToken != "access_abc" {
		t.Fatalf("want access_abc, got %s", got.Token.AccessToken)
	}
	if got.Token.RefreshToken != "refresh_xyz" {
		t.Fatalf("want refresh_xyz, got %s", got.Token.RefreshToken)
	}
}

func TestDB_ListAndDeleteInstances(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		db.SaveInstance(&ConnectorInstance{
			ID:          fmt.Sprintf("inst_%d", i),
			ConnectorID: "github",
			Status:      "active",
			CreatedAt:   time.Now(),
		})
	}

	list, err := db.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 instances, got %d", len(list))
	}

	if err := db.DeleteInstance("inst_0"); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	list, _ = db.ListInstances()
	if len(list) != 2 {
		t.Fatalf("want 2 after delete, got %d", len(list))
	}
}

// ─── Tests: Catalog Routes ────────────────────────────────────────────────────

func TestHandleListConnectors(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/connectors")
	if err != nil {
		t.Fatalf("GET /v1/connectors: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var items []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected at least 2 connectors (slack, github), got %d", len(items))
	}

	ids := make(map[string]bool)
	for _, item := range items {
		ids[item["id"].(string)] = true
	}
	if !ids["slack"] {
		t.Error("slack connector missing from catalog")
	}
	if !ids["github"] {
		t.Error("github connector missing from catalog")
	}
}

func TestHandleListConnectors_MethodNotAllowed(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Post(ts.URL+"/v1/connectors", "application/json", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}

func TestHandleInstallConnector_ReturnsOAuthRedirect(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/connectors/slack/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /v1/connectors/slack/install: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)

	if result["type"] != "oauth2" {
		t.Fatalf("want type=oauth2, got %q", result["type"])
	}
	if result["redirect_url"] == "" {
		t.Fatal("redirect_url should not be empty")
	}

	// Verify the redirect URL contains expected OAuth parameters
	parsed, err := url.Parse(result["redirect_url"])
	if err != nil {
		t.Fatalf("parse redirect_url: %v", err)
	}
	if parsed.Query().Get("state") == "" {
		t.Error("redirect_url should contain a state parameter")
	}
	if parsed.Query().Get("client_id") == "" {
		t.Error("redirect_url should contain a client_id parameter")
	}
}

func TestHandleInstallConnector_NotFound(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Post(ts.URL+"/v1/connectors/nonexistent/install", "application/json", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// ─── Tests: OAuth Callback & Full Install Flow ─────────────────────────────

// TestOAuthCallbackFlow performs the full OAuth install flow:
// 1. POST /v1/connectors/slack/install → get redirect URL with state
// 2. POST /oauth/callback?code=mock_code_12345&state={state} → creates instance
// 3. GET  /v1/instances → verify instance exists
func TestOAuthCallbackFlow_Slack(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	// Step 1: Start install to capture the state
	resp, err := http.Post(ts.URL+"/v1/connectors/slack/install", "application/json", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer resp.Body.Close()

	var installResult map[string]string
	json.NewDecoder(resp.Body).Decode(&installResult)

	parsed, _ := url.Parse(installResult["redirect_url"])
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect_url")
	}

	// Step 2: Simulate OAuth callback (mock server issues token automatically)
	callbackURL := fmt.Sprintf("%s/oauth/callback?code=mock_code_12345&state=%s", ts.URL, state)
	cbResp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer cbResp.Body.Close()

	if cbResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(cbResp.Body)
		t.Fatalf("callback returned %d: %s", cbResp.StatusCode, body)
	}

	// Step 3: List instances — should have one
	listResp, _ := http.Get(ts.URL + "/v1/instances")
	defer listResp.Body.Close()

	var instances []map[string]interface{}
	json.NewDecoder(listResp.Body).Decode(&instances)

	if len(instances) != 1 {
		t.Fatalf("want 1 instance after install, got %d", len(instances))
	}
	if instances[0]["connector_id"] != "slack" {
		t.Fatalf("want slack connector_id, got %v", instances[0]["connector_id"])
	}
	if instances[0]["status"] != "active" {
		t.Fatalf("want active status, got %v", instances[0]["status"])
	}
}

func TestOAuthCallbackFlow_InvalidState(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/oauth/callback?code=some_code&state=invalid_state_xyz")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for invalid state, got %d", resp.StatusCode)
	}
}

func TestOAuthCallbackFlow_MissingCode(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/oauth/callback?state=somestate")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for missing code, got %d", resp.StatusCode)
	}
}

// ─── Tests: Instance Management ───────────────────────────────────────────────

func TestHandleListInstances_EmptyList(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/v1/instances")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	// Must be JSON array (even if empty), never "null"
	if strings.TrimSpace(string(body)) == "null" {
		t.Fatal("want empty JSON array [], not null")
	}
	if !strings.Contains(string(body), "[") {
		t.Fatalf("want JSON array, got: %s", body)
	}
}

func TestHandleGetInstance(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	// Seed an instance directly in the DB
	inst := &ConnectorInstance{
		ID: "inst_get_test", ConnectorID: "github", Status: "active", CreatedAt: time.Now(),
	}
	gw.db.SaveInstance(inst)

	resp, _ := http.Get(ts.URL + "/v1/instances/inst_get_test")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var got map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&got)
	if got["id"] != "inst_get_test" {
		t.Fatalf("wrong ID: %v", got["id"])
	}
}

func TestHandleDeleteInstance(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	// Seed directly
	inst := &ConnectorInstance{
		ID: "inst_del_test", ConnectorID: "slack", Status: "active", CreatedAt: time.Now(),
	}
	gw.db.SaveInstance(inst)

	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/instances/inst_del_test", nil)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	// Verify it's actually gone
	getResp, _ := http.Get(ts.URL + "/v1/instances/inst_del_test")
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("after DELETE want 404, got %d", getResp.StatusCode)
	}
}

// ─── Tests: Endpoint Execution ─────────────────────────────────────────────

// installAndGetInstanceID performs the full OAuth flow and returns the new instance ID.
func installAndGetInstanceID(t *testing.T, ts *httptest.Server, connectorID string) string {
	t.Helper()

	// Start install
	installResp, err := http.Post(ts.URL+"/v1/connectors/"+connectorID+"/install", "application/json", nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer installResp.Body.Close()

	var installResult map[string]string
	json.NewDecoder(installResp.Body).Decode(&installResult)

	parsed, _ := url.Parse(installResult["redirect_url"])
	state := parsed.Query().Get("state")

	// Simulate callback
	cbURL := fmt.Sprintf("%s/oauth/callback?code=mock_code_12345&state=%s", ts.URL, state)
	cbResp, _ := http.Get(cbURL)
	cbResp.Body.Close()

	// List instances to get the ID
	listResp, _ := http.Get(ts.URL + "/v1/instances")
	defer listResp.Body.Close()

	var instances []map[string]interface{}
	json.NewDecoder(listResp.Body).Decode(&instances)

	for _, inst := range instances {
		if inst["connector_id"] == connectorID {
			return inst["id"].(string)
		}
	}

	t.Fatalf("no instance found for connector %s", connectorID)
	return ""
}

func TestHandleExecuteEndpoint_SlackSendMessage(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	instanceID := installAndGetInstanceID(t, ts, "slack")

	payload := `{"channel":"C_TEST_CHANNEL","text":"hello from gateway test"}`
	execURL := fmt.Sprintf("%s/v1/instances/%s/endpoints/send_message", ts.URL, instanceID)
	resp, err := http.Post(execURL, "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if result["ok"] != true {
		t.Fatalf("want ok=true in Slack response, got: %v", result)
	}
	if result["channel"] != "C_TEST_CHANNEL" {
		t.Fatalf("want channel=C_TEST_CHANNEL, got %v", result["channel"])
	}
}

func TestHandleExecuteEndpoint_UnknownEndpoint(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	instanceID := installAndGetInstanceID(t, ts, "slack")

	execURL := fmt.Sprintf("%s/v1/instances/%s/endpoints/nonexistent_action", ts.URL, instanceID)
	resp, _ := http.Post(execURL, "application/json", strings.NewReader("{}"))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for unknown endpoint, got %d", resp.StatusCode)
	}
}

func TestHandleExecuteEndpoint_InstanceNotFound(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, _ := http.Post(ts.URL+"/v1/instances/inst_ghost_xyz/endpoints/send_message", "application/json", strings.NewReader("{}"))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 for missing instance, got %d", resp.StatusCode)
	}
}

// ─── Tests: Webhook Receiver ──────────────────────────────────────────────────

func TestHandleWebhookPayload_SlackValid(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	// Install Slack
	instanceID := installAndGetInstanceID(t, ts, "slack")

	// Build a valid Slack-signed webhook payload
	payload := []byte(`{"event":"message","text":"hello webhook"}`)
	secret := []byte("mock_webhook_secret_value") // matches what the Slack connector uses

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req, _ := http.NewRequest("POST", ts.URL+"/webhooks/"+instanceID, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Signature", sig)
	req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestHandleWebhookPayload_InvalidSignature(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	instanceID := installAndGetInstanceID(t, ts, "slack")

	payload := []byte(`{"event":"message"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/webhooks/"+instanceID, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Signature", "v0=badhex0000000000000000000000000000000000000000000000000000000000")
	req.Header.Set("X-Slack-Request-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad signature, got %d", resp.StatusCode)
	}
}

func TestHandleWebhookPayload_InstanceNotFound(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/webhooks/inst_doesnt_exist", bytes.NewReader([]byte(`{}`)))
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// ─── Tests: Drift Detection ───────────────────────────────────────────────────

func TestFlattenJSONSchema_Basic(t *testing.T) {
	input := map[string]interface{}{
		"ok":      true,
		"channel": "C123",
		"ts":      "1234567890.123",
		"message": map[string]interface{}{
			"text": "hello",
			"user": "U123",
		},
	}

	schema := make(map[string]string)
	flattenJSONSchema("", input, schema)

	tests := map[string]string{
		"ok":           "boolean",
		"channel":      "string",
		"ts":           "string",
		"message.text": "string",
		"message.user": "string",
	}

	for key, wantType := range tests {
		if got := schema[key]; got != wantType {
			t.Errorf("key %q: want %q, got %q", key, wantType, got)
		}
	}
}

func TestFlattenJSONSchema_Array(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{
			"id":    float64(1),
			"title": "Issue 1",
			"state": "open",
		},
	}

	schema := make(map[string]string)
	flattenJSONSchema("", input, schema)

	if schema[".[].id"] != "number" {
		t.Errorf("want .[].id = number, got %q", schema[".[].id"])
	}
	if schema[".[].title"] != "string" {
		t.Errorf("want .[].title = string, got %q", schema[".[].title"])
	}
}

func TestFlattenJSONSchema_EmptyArray(t *testing.T) {
	input := []interface{}{}

	schema := make(map[string]string)
	flattenJSONSchema("root", input, schema)

	if schema["root"] != "array[empty]" {
		t.Errorf("want array[empty], got %q", schema["root"])
	}
}

func TestDriftDetector_NoDriftDetected(t *testing.T) {
	// Baseline and live are the same → no drift
	baselineJSON := `{"ok":"boolean","channel":"string","ts":"string"}`

	liveObj := map[string]interface{}{
		"ok":      true,
		"channel": "C123",
		"ts":      "123.456",
	}

	var baseline map[string]string
	json.Unmarshal([]byte(baselineJSON), &baseline)

	live := make(map[string]string)
	flattenJSONSchema("", liveObj, live)

	drifted := false
	for key, expectedType := range baseline {
		actualType, exists := live[key]
		if !exists || actualType != expectedType {
			drifted = true
			break
		}
	}

	if drifted {
		t.Error("expected no drift but drift was detected")
	}
}

func TestDriftDetector_FieldRemoved(t *testing.T) {
	// Baseline has "ts" but live response doesn't
	baselineJSON := `{"ok":"boolean","channel":"string","ts":"string"}`

	liveObj := map[string]interface{}{
		"ok":      true,
		"channel": "C123",
		// "ts" is intentionally missing → drift
	}

	var baseline map[string]string
	json.Unmarshal([]byte(baselineJSON), &baseline)

	live := make(map[string]string)
	flattenJSONSchema("", liveObj, live)

	drifted := false
	var details []string
	for key, expectedType := range baseline {
		actualType, exists := live[key]
		if !exists {
			drifted = true
			details = append(details, fmt.Sprintf("Field %q removed", key))
		} else if actualType != expectedType {
			drifted = true
			details = append(details, fmt.Sprintf("Field %q type changed %s→%s", key, expectedType, actualType))
		}
	}

	if !drifted {
		t.Error("expected drift (missing 'ts') but none detected")
	}

	found := false
	for _, d := range details {
		if strings.Contains(d, `"ts"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ts field in drift details, got: %v", details)
	}
}

func TestDriftDetector_TypeChanged(t *testing.T) {
	// Baseline has "id" as number, live returns it as string (drifted)
	baselineJSON := `{"id":"number","title":"string"}`

	liveObj := map[string]interface{}{
		"id":    "id-as-string", // now a string, was number
		"title": "some title",
	}

	var baseline map[string]string
	json.Unmarshal([]byte(baselineJSON), &baseline)

	live := make(map[string]string)
	flattenJSONSchema("", liveObj, live)

	drifted := false
	for key, expectedType := range baseline {
		actualType, exists := live[key]
		if !exists || actualType != expectedType {
			drifted = true
			_ = key
			break
		}
	}

	if !drifted {
		t.Error("expected drift (id type changed) but none detected")
	}
}

// ─── Tests: OpenAPI Spec ──────────────────────────────────────────────────────

func TestHandleOpenAPISpec(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("GET /openapi.json: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("want JSON content-type, got %q", ct)
	}

	var spec map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}

	if spec["openapi"] != "3.0.0" {
		t.Fatalf("want openapi=3.0.0, got %v", spec["openapi"])
	}
	if spec["paths"] == nil {
		t.Fatal("paths should not be nil")
	}

	paths := spec["paths"].(map[string]interface{})
	if len(paths) == 0 {
		t.Fatal("expected at least one path in OpenAPI spec")
	}

	// The send_message path should be present
	found := false
	for path := range paths {
		if strings.Contains(path, "send_message") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected send_message path in OpenAPI spec, paths: %v", func() []string {
			keys := make([]string, 0, len(paths))
			for k := range paths {
				keys = append(keys, k)
			}
			return keys
		}())
	}
}

func TestHandleDocs(t *testing.T) {
	gw, _, cleanup := newTestGateway(t)
	defer cleanup()
	ts := gatewayServer(t, gw)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/docs")
	if err != nil {
		t.Fatalf("GET /docs: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("want HTML content-type, got %q", ct)
	}
}
