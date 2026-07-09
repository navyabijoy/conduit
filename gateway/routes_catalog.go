package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"conduit/sdk"
)

// List of available connector definitions for catalog
func (g *Gateway) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	type CatalogItem struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Category    string   `json:"category"`
		Scopes      []string `json:"scopes"`
	}

	var items []CatalogItem
	for _, conn := range g.registry {
		meta := conn.Metadata()
		items = append(items, CatalogItem{
			ID:          meta.ID,
			Name:        meta.Name,
			Description: meta.Description,
			Category:    meta.Category,
			Scopes:      meta.Scopes,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// Install flow: Redirects to OAuth or takes API Key
func (g *Gateway) handleInstallConnector(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// URL shape: /v1/connectors/{connector_id}/install
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/connectors/"), "/")
	if len(parts) < 2 || parts[1] != "install" {
		writeJSONError(w, http.StatusBadRequest, "Invalid installation path")
		return
	}

	connectorID := parts[0]
	conn, ok := g.registry[connectorID]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "Connector not found")
		return
	}

	authCfg := conn.AuthConfig()
	if authCfg.Type == sdk.AuthTypeOAuth2 {
		// Start OAuth redirection
		stateBytes := make([]byte, 16)
		rand.Read(stateBytes)
		state := hex.EncodeToString(stateBytes)

		g.pendingOAuthMu.Lock()
		g.pendingOAuth[state] = connectorID
		g.pendingOAuthMu.Unlock()

		redirectURI := "http://localhost:8080/oauth/callback"
		authURL, err := url.Parse(authCfg.OAuth2.AuthURL)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Invalid authorize URL")
			return
		}

		q := authURL.Query()
		q.Set("client_id", "conduit_client_"+connectorID)
		q.Set("redirect_uri", redirectURI)
		q.Set("state", state)
		q.Set("scope", strings.Join(authCfg.OAuth2.Scopes, " "))
		authURL.RawQuery = q.Encode()

		// Return the Auth URL so the client can redirect
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"type":         "oauth2",
			"redirect_url": authURL.String(),
		})
		return
	}

	if authCfg.Type == sdk.AuthTypeAPIKey {
		// API key install: client POSTs {"api_key":"sk_..."} in the request body
		var body struct {
			APIKey string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.APIKey == "" {
			writeJSONError(w, http.StatusBadRequest, "api_key is required in request body for API key connectors")
			return
		}

		// Create instance
		instIDBytes := make([]byte, 8)
		rand.Read(instIDBytes)
		instanceID := fmt.Sprintf("inst_%s_%s", connectorID, hex.EncodeToString(instIDBytes))

		inst := &ConnectorInstance{
			ID:            instanceID,
			ConnectorID:   connectorID,
			Status:        "active",
			WebhookSecret: g.webhookSecret,
			CreatedAt:     time.Now(),
		}

		creds := &sdk.Credentials{APIKey: body.APIKey}
		if err := g.db.SaveCredentials(instanceID, sdk.AuthTypeAPIKey, creds); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Failed to save credentials")
			return
		}

		// Generate baseline schema
		baselineJSON, err := g.generateBaseline(instanceID, conn, creds)
		if err != nil {
			log.Printf("WARNING: Failed to generate baseline schema: %v", err)
		} else {
			inst.BaselineSchema = baselineJSON
		}

		if err := g.db.SaveInstance(inst); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Failed to save connector instance")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"type":        "apikey",
			"instance_id": instanceID,
			"status":      "active",
		})
		return
	}

	writeJSONError(w, http.StatusBadRequest, "Unsupported auth type")
}

// OAuth Callback handler
func (g *Gateway) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if code == "" || state == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing code or state")
		return
	}

	g.pendingOAuthMu.Lock()
	connectorID, ok := g.pendingOAuth[state]
	if ok {
		delete(g.pendingOAuth, state)
	}
	g.pendingOAuthMu.Unlock()

	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid state token")
		return
	}

	conn := g.registry[connectorID]
	authCfg := conn.AuthConfig()

	// Perform Code-to-Token Exchange
	tokenURL := authCfg.OAuth2.TokenURL
	client := &http.Client{Timeout: 10 * time.Second}
	
	// Create POST parameters
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", "conduit_client_"+connectorID)
	form.Set("client_secret", "conduit_secret_"+connectorID)
	form.Set("redirect_uri", "http://localhost:8080/oauth/callback")

	resp, err := client.PostForm(tokenURL, form)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "Failed to exchange authorization code: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("Token exchange returned status %d: %s", resp.StatusCode, string(bodyBytes)))
		return
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to parse token response")
		return
	}

	token := &sdk.Token{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}

	// Create Instance ID
	instIDBytes := make([]byte, 8)
	rand.Read(instIDBytes)
	instanceID := fmt.Sprintf("inst_%s_%s", connectorID, hex.EncodeToString(instIDBytes))

	// Save Instance Record
	inst := &ConnectorInstance{
		ID:            instanceID,
		ConnectorID:   connectorID,
		Status:        "active",
		WebhookSecret: g.webhookSecret,
		CreatedAt:     time.Now(),
	}

	// Save credentials
	creds := &sdk.Credentials{Token: token}
	if err := g.db.SaveCredentials(instanceID, sdk.AuthTypeOAuth2, creds); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to save credentials")
		return
	}

	// Generate baseline schema
	baselineJSON, err := g.generateBaseline(instanceID, conn, creds)
	if err != nil {
		log.Printf("WARNING: Failed to generate baseline schema: %v", err)
	} else {
		inst.BaselineSchema = baselineJSON
	}

	if err := g.db.SaveInstance(inst); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to save connector instance")
		return
	}

	// Show success page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := fmt.Sprintf(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>Installation Successful</title>
			<style>
				body { font-family: -apple-system, sans-serif; background: #0f172a; color: #f8fafc; padding: 2rem; display: flex; justify-content: center; align-items: center; height: 80vh; margin: 0; }
				.card { background: #1e293b; border: 1px solid #334155; padding: 2rem; border-radius: 12px; width: 400px; box-shadow: 0 4px 6px -1px rgba(0,0,0,0.1); text-align: center; }
				h2 { margin-top: 0; color: #10b981; }
				.info { margin: 1rem 0; color: #94a3b8; font-size: 0.9rem; line-height: 1.4; }
				.btn { display: inline-block; padding: 0.75rem 1.5rem; background: #6366f1; border: none; color: white; border-radius: 6px; font-weight: bold; cursor: pointer; text-decoration: none; margin-top: 1rem; }
				.btn:hover { background: #4f46e5; }
			</style>
		</head>
		<body>
			<div class="card">
				<h2>Installation Successful!</h2>
				<p class="info">Connector <strong>%s</strong> was installed successfully.</p>
				<p class="info">Instance ID: <code>%s</code></p>
				<p class="info">You can now close this tab and return to the main dashboard.</p>
				<a href="/ui/" class="btn">Go to Dashboard</a>
			</div>
		</body>
		</html>
	`, conn.Metadata().Name, instanceID)
	w.Write([]byte(html))
}

// Lists all installed connector instances
func (g *Gateway) handleListInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	list, err := g.db.ListInstances()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to load instances: "+err.Error())
		return
	}

	// Ensure we return empty list [] instead of null JSON if empty
	if list == nil {
		list = []*ConnectorInstance{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// Router dispatching GET, DELETE, and execution on /v1/instances/{id}
func (g *Gateway) handleInstanceRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/instances/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing instance ID")
		return
	}

	instanceID := parts[0]

	// 1. DELETE Instance: /v1/instances/{id}
	if r.Method == "DELETE" && len(parts) == 1 {
		err := g.db.DeleteInstance(instanceID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Failed to delete instance: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"deleted"}`))
		return
	}

	// 2. GET Instance: /v1/instances/{id}
	if r.Method == "GET" && len(parts) == 1 {
		inst, err := g.db.GetInstance(instanceID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "Instance not found: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inst)
		return
	}

	// 3. EXECUTE Endpoint: /v1/instances/{id}/endpoints/{endpoint_name}
	if r.Method == "POST" && len(parts) >= 3 && parts[1] == "endpoints" {
		endpointName := parts[2]
		g.handleExecuteEndpoint(w, r, instanceID, endpointName)
		return
	}

	writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
}

// generateBaseline executes a test request to capture schema structure upon installation
func (g *Gateway) generateBaseline(instanceID string, conn sdk.Connector, creds *sdk.Credentials) (string, error) {
	log.Printf("[Gateway] Capturing baseline schema for instance %s", instanceID)
	
	// Create client
	client := sdk.NewHTTPClient(
		"http://"+g.mockHost,
		*creds,
		conn.AuthConfig(),
		g.tokenRefreshHook(instanceID, conn),
	)

	var payload []byte
	var err error

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if conn.Metadata().ID == "slack" {
		req := sdk.Request{
			Body: []byte(`{"channel":"C_BASELINE_GEN","text":"Baseline probe"}`),
		}
		var resp sdk.Response
		resp, err = conn.Endpoints()[0].Execute(ctx, client, req)
		if err == nil {
			payload = resp.Body
		}
	} else if conn.Metadata().ID == "github" {
		// Run list_issues request on GitHub mock
		req := sdk.Request{
			Params: map[string]string{
				"owner": "baseline_owner",
				"repo":  "baseline_repo",
			},
		}
		// list_issues is endpoint index 1
		var listEP *sdk.EndpointDefinition
		for _, ep := range conn.Endpoints() {
			if ep.Name == "list_issues" {
				listEP = &ep
				break
			}
		}
		if listEP != nil {
			var resp sdk.Response
			resp, err = listEP.Execute(ctx, client, req)
			if err == nil {
				payload = resp.Body
			}
		}
	} else if conn.Metadata().ID == "stripe" {
		req := sdk.Request{Params: map[string]string{"limit": "3"}}
		for _, ep := range conn.Endpoints() {
			if ep.Name == "list_charges" {
				var resp sdk.Response
				resp, err = ep.Execute(ctx, client, req)
				if err == nil {
					payload = resp.Body
				}
				break
			}
		}
	}

	if err != nil {
		return "", fmt.Errorf("baseline request failed: %w", err)
	}

	// Unmarshal and flatten
	var obj interface{}
	if err := json.Unmarshal(payload, &obj); err != nil {
		return "", fmt.Errorf("failed to unmarshal baseline response: %w", err)
	}

	flatSchema := make(map[string]string)
	flattenJSONSchema("", obj, flatSchema)

	schemaBytes, err := json.Marshal(flatSchema)
	if err != nil {
		return "", err
	}
	return string(schemaBytes), nil
}
