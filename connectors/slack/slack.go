package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"conduit/sdk"
)

type SlackConnector struct {
	mockBaseURL string
}

func NewSlackConnector(mockBaseURL string) *SlackConnector {
	if mockBaseURL == "" {
		mockBaseURL = "localhost:8081"
	}
	return &SlackConnector{mockBaseURL: mockBaseURL}
}

type SendMessageInput struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

type SendMessageOutput struct {
	OK      bool   `json:"ok"`
	Channel string `json:"channel"`
	TS      string `json:"ts,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (c *SlackConnector) Metadata() sdk.ConnectorMetadata {
	return sdk.ConnectorMetadata{
		ID:          "slack",
		Name:        "Slack",
		Description: "Send messages and receive events from Slack workspaces.",
		Category:    "Communication",
		Scopes:      []string{"chat:write", "channels:read", "incoming-webhook"},
	}
}

func (c *SlackConnector) AuthConfig() sdk.AuthConfig {
	return sdk.AuthConfig{
		Type: sdk.AuthTypeOAuth2,
		OAuth2: &sdk.OAuth2Config{
			AuthURL:  fmt.Sprintf("http://%s/mock/oauth/authorize", c.mockBaseURL),
			TokenURL: fmt.Sprintf("http://%s/services/slack/oauth/token", c.mockBaseURL),
			Scopes:   []string{"chat:write", "channels:read"},
		},
	}
}

func (c *SlackConnector) Endpoints() []sdk.EndpointDefinition {
	return []sdk.EndpointDefinition{
		{
			Name:         "send_message",
			Method:       "POST",
			Path:         "/services/slack/chat.postMessage",
			Description:  "Send a message to a channel.",
			InputSchema:  &SendMessageInput{},
			OutputSchema: &SendMessageOutput{},
			Execute:      c.executeSendMessage,
		},
	}
}

func (c *SlackConnector) executeSendMessage(ctx context.Context, client *sdk.HTTPClient, req sdk.Request) (sdk.Response, error) {
	// Parse input
	var input SendMessageInput
	if err := json.Unmarshal(req.Body, &input); err != nil {
		return sdk.Response{}, sdk.NewPermanentError("invalid JSON body", http.StatusBadRequest, err)
	}

	if input.Channel == "" || input.Text == "" {
		return sdk.Response{}, sdk.NewPermanentError("channel and text are required fields", http.StatusBadRequest, nil)
	}

	// Prepare raw payload
	payloadBytes, err := json.Marshal(input)
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to marshal request", http.StatusInternalServerError, err)
	}

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "/services/slack/chat.postMessage", strings.NewReader(string(payloadBytes)))
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to create HTTP request", http.StatusInternalServerError, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Execute call
	resp, err := client.Do(httpReq)
	if err != nil {
		return sdk.Response{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sdk.Response{}, sdk.NewTransientError("failed to read response body", resp.StatusCode, err)
	}

	return sdk.Response{
		Body:       body,
		StatusCode: resp.StatusCode,
	}, nil
}

func (c *SlackConnector) HandleWebhook(ctx context.Context, event sdk.WebhookEvent) error {
	// Slack signs webhooks with X-Slack-Signature and uses raw request body
	sig := event.Headers["X-Slack-Signature"]
	if sig == "" {
		return sdk.NewPermanentError("missing Slack signature header", http.StatusBadRequest, nil)
	}

	// Signatures usually look like v0=hex...
	// We'll read the webhook secret from context or system environment (simulated here)
	secret := []byte("mock_webhook_secret_value")

	if !sdk.VerifyHMACSHA256HexSignature(event.Payload, sig, "v0=", secret) {
		return sdk.NewPermanentError("invalid webhook signature", http.StatusUnauthorized, nil)
	}

	// Signature matches! Parse the event payload.
	// (Business logic would process this event, e.g. logging or forwarding. For the SDK we just validate).
	return nil
}
