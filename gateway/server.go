package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"conduit/connectors/github"
	"conduit/connectors/slack"
	"conduit/connectors/stripe"
	"conduit/sdk"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Gateway struct {
	db             *DB
	mockHost       string
	webhookSecret  string
	registry       map[string]sdk.Connector
	pendingOAuth   map[string]string // state -> connectorID
	pendingOAuthMu sync.Mutex
	driftDetector  *DriftDetector
}

func NewGateway(db *DB, mockHost string, webhookSecret string) *Gateway {
	registry := map[string]sdk.Connector{
		"slack":  slack.NewSlackConnector(mockHost),
		"github": github.NewGitHubConnector(mockHost),
		"stripe": stripe.NewStripeConnector(mockHost),
	}

	g := &Gateway{
		db:            db,
		mockHost:      mockHost,
		webhookSecret: webhookSecret,
		registry:      registry,
		pendingOAuth:  make(map[string]string),
	}

	g.driftDetector = NewDriftDetector(db, registry, webhookSecret, mockHost)
	return g
}

func (g *Gateway) Start(addr string) {
	mux := http.NewServeMux()

	// Catalog & OAuth Install Routes
	mux.HandleFunc("/v1/connectors", g.handleListConnectors)
	mux.HandleFunc("/v1/connectors/", g.handleInstallConnector) // matches /v1/connectors/{id}/install
	mux.HandleFunc("/oauth/callback", g.handleOAuthCallback)

	// Instances Management
	mux.HandleFunc("/v1/instances", g.handleListInstances)
	mux.HandleFunc("/v1/instances/", g.handleInstanceRoute) // matches GET /v1/instances/{id}, DELETE /v1/instances/{id}, or POST /v1/instances/{id}/endpoints/{endpoint}

	// Global Webhook Receiver
	mux.HandleFunc("/webhooks/", g.handleWebhookPayload)

	// OpenAPI and Metrics
	mux.HandleFunc("/openapi.json", g.handleOpenAPISpec)
	mux.Handle("/metrics", promhttp.Handler())

	// UI Dashboard — serves static files from ./ui/
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("./ui"))))

	// Root redirect to /ui/
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// Start Drift Detector background worker
	g.driftDetector.Start(30 * time.Second)

	log.Printf("[Gateway] Starting API Gateway on %s", addr)
	server := &http.Server{
		Addr:    addr,
		Handler: g.loggingMiddleware(mux),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Gateway] Failed: %v", err)
		}
	}()
}

func (g *Gateway) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[Gateway] %s %s - %v", r.Method, r.URL.Path, time.Since(start))
	})
}

// tokenRefreshHook handles renewing OAuth tokens.
func (g *Gateway) tokenRefreshHook(instanceID string, conn sdk.Connector) func(ctx context.Context, token *sdk.Token) (*sdk.Token, error) {
	return func(ctx context.Context, oldToken *sdk.Token) (*sdk.Token, error) {
		log.Printf("[Gateway] Refreshing OAuth token for instance %s", instanceID)
		
		authCfg := conn.AuthConfig()
		if authCfg.Type != sdk.AuthTypeOAuth2 || authCfg.OAuth2 == nil {
			return nil, fmt.Errorf("connector does not support OAuth2")
		}

		// Prepare refresh request
		// Since it's a mock/local service, we can post refresh parameters
		client := &http.Client{Timeout: 10 * time.Second}
		
		// In a real OAuth server, we would send client ID / secret / refresh_token
		// We'll simulate this token trade with our mock token URL
		resp, err := client.Post(authCfg.OAuth2.TokenURL+"?grant_type=refresh_token&refresh_token="+oldToken.RefreshToken, "application/json", nil)
		if err != nil {
			RecordTokenRefresh(conn.Metadata().ID, "failure")
			return nil, fmt.Errorf("failed to call refresh URL: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			RecordTokenRefresh(conn.Metadata().ID, "failure")
			return nil, fmt.Errorf("refresh server returned code %d", resp.StatusCode)
		}

		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			RecordTokenRefresh(conn.Metadata().ID, "failure")
			return nil, fmt.Errorf("failed to parse refresh response: %w", err)
		}

		newToken := &sdk.Token{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			Expiry:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		}

		// Encrypt and save back to DB
		creds := &sdk.Credentials{Token: newToken}
		if err := g.db.SaveCredentials(instanceID, sdk.AuthTypeOAuth2, creds); err != nil {
			RecordTokenRefresh(conn.Metadata().ID, "failure")
			return nil, fmt.Errorf("failed to save refreshed credentials: %w", err)
		}

		RecordTokenRefresh(conn.Metadata().ID, "success")
		log.Printf("[Gateway] Token refreshed successfully for instance %s", instanceID)
		return newToken, nil
	}
}

// Helpers for responses
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
