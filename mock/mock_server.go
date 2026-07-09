package mock

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MockServer struct {
	addr             string
	mu               sync.RWMutex
	driftSlack       bool
	driftGitHub      bool
	driftStripe      bool
	rateLimitSlack   bool
	rateLimitGitHub  bool
	rateLimitStripe  bool
	webhookSecret    string
}

func NewMockServer(addr string, webhookSecret string) *MockServer {
	return &MockServer{
		addr:          addr,
		webhookSecret: webhookSecret,
	}
}

func (s *MockServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// OAuth Endpoints
	mux.HandleFunc("/mock/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/services/slack/oauth/token", s.handleSlackToken)
	mux.HandleFunc("/services/github/login/oauth/access_token", s.handleGitHubToken)

	// Slack Mock API
	mux.HandleFunc("/services/slack/chat.postMessage", s.handleSlackPostMessage)

	// GitHub Mock API
	mux.HandleFunc("/services/github/repos/", s.handleGitHubIssues)

	// Stripe Mock API
	mux.HandleFunc("/services/stripe/v1/charges", s.handleStripeListCharges)

	// Debug/Simulation Endpoints
	mux.HandleFunc("/mock/toggle-drift", s.handleToggleDrift)
	mux.HandleFunc("/mock/toggle-rate-limit", s.handleToggleRateLimit)
	mux.HandleFunc("/mock/trigger-webhook", s.handleTriggerWebhook)

	return mux
}

func (s *MockServer) Start() {
	server := &http.Server{
		Addr:    s.addr,
		Handler: s.Handler(),
	}

	log.Printf("[Mock Server] Starting on %s", s.addr)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[Mock Server] Failed: %v", err)
		}
	}()
}

func (s *MockServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	scopes := r.URL.Query().Get("scope")
	clientID := r.URL.Query().Get("client_id")

	// Render a simple HTML page to simulate consent
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := fmt.Sprintf(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>Mock OAuth Consent</title>
			<style>
				body { font-family: -apple-system, sans-serif; background: #0f172a; color: #f8fafc; padding: 2rem; display: flex; justify-content: center; align-items: center; height: 80vh; margin: 0; }
				.card { background: #1e293b; border: 1px solid #334155; padding: 2rem; border-radius: 12px; width: 400px; box-shadow: 0 4px 6px -1px rgba(0,0,0,0.1); }
				h2 { margin-top: 0; color: #818cf8; }
				.info { margin: 1rem 0; color: #94a3b8; font-size: 0.9rem; line-height: 1.4; }
				.btn { display: block; width: 100%%; padding: 0.75rem; background: #6366f1; border: none; color: white; border-radius: 6px; font-weight: bold; cursor: pointer; text-align: center; text-decoration: none; }
				.btn:hover { background: #4f46e5; }
			</style>
		</head>
		<body>
			<div class="card">
				<h2>Authorize Integration</h2>
				<p class="info">Client ID: <code>%s</code></p>
				<p class="info">Requested Scopes: <code>%s</code></p>
				<p class="info">This is a simulated OAuth screen. Click the button to grant access and return to Conduit.</p>
				<a href="%s?code=mock_code_12345&state=%s" class="btn">Authorize & Connect</a>
			</div>
		</body>
		</html>
	`, clientID, scopes, redirectURI, state)
	w.Write([]byte(html))
}

func (s *MockServer) handleSlackToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"access_token":  "mock_slack_access_token_" + strconv.FormatInt(time.Now().Unix(), 10),
		"refresh_token": "mock_slack_refresh_token_xyz",
		"expires_in":    3600,
		"ok":            true,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *MockServer) handleGitHubToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"access_token":  "mock_github_access_token_" + strconv.FormatInt(time.Now().Unix(), 10),
		"refresh_token": "mock_github_refresh_token_xyz",
		"expires_in":    3600,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *MockServer) handleSlackPostMessage(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	rateLimit := s.rateLimitSlack
	drift := s.driftSlack
	s.mu.RUnlock()

	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"ok":false, "error":"not_authed"}`))
		return
	}

	if rateLimit {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false, "error":"ratelimited"}`))
		return
	}

	var reqBody map[string]interface{}
	bodyBytes, _ := io.ReadAll(r.Body)
	json.Unmarshal(bodyBytes, &reqBody)

	w.Header().Set("Content-Type", "application/json")

	if drift {
		// Drifted Slack response: channel -> channel_id_drifted, removes message ts
		resp := map[string]interface{}{
			"ok":                  true,
			"channel_id_drifted":  reqBody["channel"],
			"message_text":        reqBody["text"],
			"drift_indicator_xyz": true,
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Normal Slack response
	resp := map[string]interface{}{
		"ok":      true,
		"channel": reqBody["channel"],
		"ts":      "1234567890.123456",
		"message": map[string]interface{}{
			"text": reqBody["text"],
			"type": "message",
			"user": "U12345",
		},
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *MockServer) handleGitHubIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	rateLimit := s.rateLimitGitHub
	drift := s.driftGitHub
	s.mu.RUnlock()

	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if rateLimit {
		resetTime := time.Now().Unix() + 2
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime, 10))
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	// Expected paths:
	// POST /services/github/repos/{owner}/{repo}/issues (create issue)
	// GET /services/github/repos/{owner}/{repo}/issues (list issues)
	
	isList := r.Method == "GET" && len(parts) >= 7 && parts[6] == "issues"
	isCreate := r.Method == "POST" && len(parts) >= 7 && parts[6] == "issues"

	w.Header().Set("Content-Type", "application/json")

	if isCreate {
		var reqBody map[string]interface{}
		bodyBytes, _ := io.ReadAll(r.Body)
		json.Unmarshal(bodyBytes, &reqBody)

		if drift {
			// Drifted GitHub issue response: missing 'number' field
			resp := map[string]interface{}{
				"id":    999,
				"title": reqBody["title"],
				"body":  reqBody["body"],
				"state": "open",
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]interface{}{
			"id":     999,
			"number": 1,
			"title":  reqBody["title"],
			"body":   reqBody["body"],
			"state":  "open",
			"user": map[string]interface{}{
				"login": "mockuser",
			},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	if isList {
		cursor := r.URL.Query().Get("cursor")

		if drift {
			// Drifted list issues: state is missing or renamed, id changes to string
			if cursor == "" || cursor == "page1" {
				w.Header().Set("Link", `</services/github/repos/owner/repo/issues?cursor=page2>; rel="next"`)
				resp := []map[string]interface{}{
					{"id": "issue-1", "title": "First Issue"},
					{"id": "issue-2", "title": "Second Issue"},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}
			resp := []map[string]interface{}{
				{"id": "issue-3", "title": "Third Issue"},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		if cursor == "" || cursor == "page1" {
			// Return link header for page 2
			w.Header().Set("Link", `</services/github/repos/owner/repo/issues?cursor=page2>; rel="next"`)
			resp := []map[string]interface{}{
				{"id": 1, "number": 1, "title": "First Issue", "state": "open"},
				{"id": 2, "number": 2, "title": "Second Issue", "state": "open"},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		
		resp := []map[string]interface{}{
			{"id": 3, "number": 3, "title": "Third Issue", "state": "closed"},
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

// handleStripeListCharges implements GET /services/stripe/v1/charges
// Stripe uses API key auth (Authorization: Bearer sk_...) and offset pagination (?starting_after=ch_xxx&limit=N)
func (s *MockServer) handleStripeListCharges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	rateLimit := s.rateLimitStripe
	drift := s.driftStripe
	s.mu.RUnlock()

	// API key validation
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer sk_") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"No valid API key provided"}}`))
		return
	}

	if rateLimit {
		w.Header().Set("Retry-After", "2")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"}}`))
		return
	}

	q := r.URL.Query()
	startingAfter := q.Get("starting_after")
	limit := 3

	w.Header().Set("Content-Type", "application/json")

	if drift {
		// Drifted: amount becomes string, currency removed
		data := []map[string]interface{}{
			{"id": "ch_drift_1", "amount_str": "2000", "status": "succeeded"},
			{"id": "ch_drift_2", "amount_str": "5000", "status": "succeeded"},
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object":   "list",
			"data":     data,
			"has_more": false,
		})
		return
	}

	// Normal Stripe paginated response
	var charges []map[string]interface{}
	if startingAfter == "" {
		// First page
		for i := 1; i <= limit; i++ {
			charges = append(charges, map[string]interface{}{
				"id":       fmt.Sprintf("ch_page1_%d", i),
				"amount":   1000 * i,
				"currency": "usd",
				"status":   "succeeded",
				"created":  time.Now().Unix() - int64(i*3600),
			})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object":   "list",
			"data":     charges,
			"has_more": true,
			"url":      "/services/stripe/v1/charges",
		})
	} else {
		// Second page
		for i := 1; i <= 2; i++ {
			charges = append(charges, map[string]interface{}{
				"id":       fmt.Sprintf("ch_page2_%d", i),
				"amount":   5000 * i,
				"currency": "usd",
				"status":   "succeeded",
				"created":  time.Now().Unix() - int64((i+3)*3600),
			})
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"object":   "list",
			"data":     charges,
			"has_more": false,
			"url":      "/services/stripe/v1/charges",
		})
	}
}

func (s *MockServer) handleToggleDrift(w http.ResponseWriter, r *http.Request) {
	connectorID := r.URL.Query().Get("connector_id")
	driftStr := r.URL.Query().Get("drift")
	drift, _ := strconv.ParseBool(driftStr)

	s.mu.Lock()
	defer s.mu.Unlock()

	if connectorID == "slack" {
		s.driftSlack = drift
	} else if connectorID == "github" {
		s.driftGitHub = drift
	} else if connectorID == "stripe" {
		s.driftStripe = drift
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"connector_id":"%s","drift":%v}`, connectorID, drift)
}

func (s *MockServer) handleToggleRateLimit(w http.ResponseWriter, r *http.Request) {
	connectorID := r.URL.Query().Get("connector_id")
	rateLimitStr := r.URL.Query().Get("rate_limit")
	rateLimit, _ := strconv.ParseBool(rateLimitStr)

	s.mu.Lock()
	defer s.mu.Unlock()

	if connectorID == "slack" {
		s.rateLimitSlack = rateLimit
	} else if connectorID == "github" {
		s.rateLimitGitHub = rateLimit
	} else if connectorID == "stripe" {
		s.rateLimitStripe = rateLimit
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"connector_id":"%s","rate_limit":%v}`, connectorID, rateLimit)
}

func (s *MockServer) handleTriggerWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectorID string                 `json:"connector_id"`
		TargetURL   string                 `json:"target_url"`
		Payload     map[string]interface{} `json:"payload"`
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"cannot read body"}`))
		return
	}
	json.Unmarshal(bodyBytes, &req)

	payloadBytes, _ := json.Marshal(req.Payload)

	// Sign payload using hmac-sha256 and s.webhookSecret
	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	mac.Write(payloadBytes)
	signatureHex := hex.EncodeToString(mac.Sum(nil))

	webhookReq, err := http.NewRequest("POST", req.TargetURL, bytes.NewReader(payloadBytes))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"error":"%v"}`, err)))
		return
	}

	webhookReq.Header.Set("Content-Type", "application/json")
	if req.ConnectorID == "slack" {
		webhookReq.Header.Set("X-Slack-Signature", "v0="+signatureHex)
		webhookReq.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	} else if req.ConnectorID == "github" {
		webhookReq.Header.Set("X-Hub-Signature-256", "sha256="+signatureHex)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(webhookReq)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(fmt.Sprintf(`{"error":"failed to dispatch webhook: %v"}`, err)))
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}
