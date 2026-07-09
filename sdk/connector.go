package sdk

import "context"

// Connector is the interface that every SaaS integration must implement.
type Connector interface {
	Metadata() ConnectorMetadata
	AuthConfig() AuthConfig
	Endpoints() []EndpointDefinition
	HandleWebhook(ctx context.Context, event WebhookEvent) error
}

// ConnectorMetadata describes the connector.
type ConnectorMetadata struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Scopes      []string `json:"scopes"`
}

// AuthType represents the method of authentication used by the connector.
type AuthType string

const (
	AuthTypeOAuth2 AuthType = "oauth2"
	AuthTypeAPIKey AuthType = "apikey"
)

// AuthConfig holds auth setup details for the integration.
type AuthConfig struct {
	Type   AuthType      `json:"type"`
	OAuth2 *OAuth2Config `json:"oauth2,omitempty"`
	APIKey *APIKeyConfig `json:"apikey,omitempty"`
}

// OAuth2Config holds the URLs and static settings needed for OAuth2.
type OAuth2Config struct {
	AuthURL  string   `json:"auth_url"`
	TokenURL string   `json:"token_url"`
	Scopes   []string `json:"scopes"`
}

// APIKeyConfig defines the header key and optional prefix (e.g. Bearer) for API-key based auth.
type APIKeyConfig struct {
	HeaderName  string `json:"header_name"`
	ValuePrefix string `json:"value_prefix"`
}

// Request contains the payload and parameters for a connector execution.
type Request struct {
	Body   []byte
	Params map[string]string
}

// Response contains the response details returned by a connector execution.
type Response struct {
	Body       []byte
	StatusCode int
}

// EndpointDefinition represents a schema-aware versioned endpoint exposed by the connector.
type EndpointDefinition struct {
	Name         string
	Method       string
	Path         string
	Description  string
	InputSchema  interface{} // E.g., &SomeInputStruct{}
	OutputSchema interface{} // E.g., &SomeOutputStruct{}
	Execute      func(ctx context.Context, client *HTTPClient, req Request) (Response, error)
	GetPaginator func(path string, params map[string]string, limit int) Paginator
}

// WebhookEvent represents an inbound webhook payload with verification context.
type WebhookEvent struct {
	Payload   []byte
	Headers   map[string]string
	EventType string
}
