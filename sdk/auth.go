package sdk

import (
	"time"
)

// Token represents OAuth2 credentials.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// IsExpired checks if the token is expired or close to expiring (using a 1-minute buffer).
func (t *Token) IsExpired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(1 * time.Minute).After(t.Expiry)
}

// Credentials represents the auth state passed to the connector.
type Credentials struct {
	Token  *Token
	APIKey string
}
