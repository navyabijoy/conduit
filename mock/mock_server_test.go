package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockServerOAuthAndAPI(t *testing.T) {
	s := NewMockServer("127.0.0.1:0", "webhook_secret_test") // use port 0 for dynamic port allocation
	
	mux := http.NewServeMux()
	mux.HandleFunc("/mock/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/services/slack/oauth/token", s.handleSlackToken)
	mux.HandleFunc("/services/github/login/oauth/access_token", s.handleGitHubToken)
	mux.HandleFunc("/services/slack/chat.postMessage", s.handleSlackPostMessage)
	mux.HandleFunc("/services/github/repos/", s.handleGitHubIssues)
	mux.HandleFunc("/mock/toggle-drift", s.handleToggleDrift)
	mux.HandleFunc("/mock/toggle-rate-limit", s.handleToggleRateLimit)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 1. Test Slack token exchange
	resp, err := http.Post(ts.URL+"/services/slack/oauth/token", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to call token: %v", err)
	}
	var slackToken map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&slackToken)
	resp.Body.Close()

	if slackToken["access_token"] == nil || !strings.Contains(slackToken["access_token"].(string), "mock_slack_access_token_") {
		t.Errorf("unexpected access token: %v", slackToken)
	}

	// 2. Test Slack post message
	req, _ := http.NewRequest("POST", ts.URL+"/services/slack/chat.postMessage", bytes.NewBufferString(`{"channel":"C123","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer mock_slack_token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to call chat.postMessage: %v", err)
	}
	var slackPost map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&slackPost)
	resp.Body.Close()

	if slackPost["ok"] != true || slackPost["channel"] != "C123" {
		t.Errorf("unexpected post response: %v", slackPost)
	}

	// 3. Test Slack post message rate limited
	// Toggle rate limit
	toggleURL := fmt.Sprintf("%s/mock/toggle-rate-limit?connector_id=slack&rate_limit=true", ts.URL)
	http.Post(toggleURL, "application/json", nil)

	req, _ = http.NewRequest("POST", ts.URL+"/services/slack/chat.postMessage", bytes.NewBufferString(`{"channel":"C123","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer mock_slack_token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to call chat.postMessage: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 too many requests, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "1" {
		t.Errorf("expected Retry-After: 1 header, got %q", resp.Header.Get("Retry-After"))
	}

	// Untoggle rate limit
	toggleURL = fmt.Sprintf("%s/mock/toggle-rate-limit?connector_id=slack&rate_limit=false", ts.URL)
	http.Post(toggleURL, "application/json", nil)

	// 4. Test Slack post message drift
	toggleDriftURL := fmt.Sprintf("%s/mock/toggle-drift?connector_id=slack&drift=true", ts.URL)
	http.Post(toggleDriftURL, "application/json", nil)

	req, _ = http.NewRequest("POST", ts.URL+"/services/slack/chat.postMessage", bytes.NewBufferString(`{"channel":"C123","text":"hello"}`))
	req.Header.Set("Authorization", "Bearer mock_slack_token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to call chat.postMessage: %v", err)
	}
	var slackDrift map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&slackDrift)
	resp.Body.Close()

	if slackDrift["channel_id_drifted"] != "C123" || slackDrift["channel"] != nil {
		t.Errorf("expected drifted schema response, got: %v", slackDrift)
	}
}
