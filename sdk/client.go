package sdk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPClient wraps http.Client to handle credentials, retries, and rate limits.
type HTTPClient struct {
	client           *http.Client
	baseURL          string
	creds            Credentials
	authConfig       AuthConfig
	tokenRefreshHook func(ctx context.Context, token *Token) (*Token, error)
	maxRetries       int
	baseBackoff      time.Duration
	maxBackoff       time.Duration
}

// NewHTTPClient creates a configured HTTPClient.
func NewHTTPClient(
	baseURL string,
	creds Credentials,
	authConfig AuthConfig,
	tokenRefreshHook func(ctx context.Context, token *Token) (*Token, error),
) *HTTPClient {
	return &HTTPClient{
		client:           &http.Client{Timeout: 30 * time.Second},
		baseURL:          baseURL,
		creds:            creds,
		authConfig:       authConfig,
		tokenRefreshHook: tokenRefreshHook,
		maxRetries:       5,
		baseBackoff:      100 * time.Millisecond,
		maxBackoff:       10 * time.Second,
	}
}

// SetMaxRetries overrides the default maximum retry count.
func (c *HTTPClient) SetMaxRetries(r int) {
	c.maxRetries = r
}

// SetBackoffRange overrides the backoff configuration.
func (c *HTTPClient) SetBackoffRange(base, max time.Duration) {
	c.baseBackoff = base
	c.maxBackoff = max
}

// Credentials returns the current credentials.
func (c *HTTPClient) Credentials() Credentials {
	return c.creds
}

// Do executes an HTTP request, applying credentials, retries, and rate-limit handling.
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Clone the request because we will modify it (headers) and potentially retry it.
	cloneRequest := func(r *http.Request, body []byte) *http.Request {
		rClone := r.Clone(r.Context())
		if len(body) > 0 {
			rClone.Body = io.NopCloser(bytes.NewReader(body))
		}
		return rClone
	}

	// Read body so we can re-send it on retries
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, NewPermanentError("failed to read request body", 0, err)
		}
	}

	hasRefreshed := false

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		// 1. Apply credentials / Refresh if expired
		if c.authConfig.Type == AuthTypeOAuth2 && c.creds.Token != nil {
			if c.creds.Token.IsExpired() && c.tokenRefreshHook != nil && !hasRefreshed {
				newToken, err := c.tokenRefreshHook(ctx, c.creds.Token)
				if err != nil {
					return nil, NewPermanentError("failed to refresh token pre-emptively", 0, err)
				}
				c.creds.Token = newToken
				hasRefreshed = true
			}
		}

		currentReq := cloneRequest(req, bodyBytes)
		c.applyAuth(currentReq)

		// 2. Perform Request
		resp, err := c.client.Do(currentReq)
		if err != nil {
			if attempt == c.maxRetries {
				return nil, NewTransientError("request failed after retries", 0, err)
			}
			if IsTransient(err) {
				c.sleep(ctx, attempt, 0)
				continue
			}
			return nil, NewPermanentError("network request failed permanently", 0, err)
		}

		// 3. Handle Token Refresh on 401 Unauthorized
		if resp.StatusCode == http.StatusUnauthorized && c.authConfig.Type == AuthTypeOAuth2 && c.tokenRefreshHook != nil && !hasRefreshed {
			resp.Body.Close()
			newToken, err := c.tokenRefreshHook(ctx, c.creds.Token)
			if err != nil {
				return nil, NewPermanentError("failed to refresh token on 401", http.StatusUnauthorized, err)
			}
			c.creds.Token = newToken
			hasRefreshed = true
			attempt-- // Redo the attempt with the new token
			continue
		}

		// 4. Handle Rate Limiting
		var rateLimitSleep time.Duration
		isRateLimited := false

		// Slack format: 429 status code with Retry-After header
		if resp.StatusCode == http.StatusTooManyRequests {
			isRateLimited = true
			if retryAfterHeader := resp.Header.Get("Retry-After"); retryAfterHeader != "" {
				rateLimitSleep = parseRetryAfter(retryAfterHeader)
			}
		}

		// GitHub format: 403/429 status code with X-RateLimit-Remaining: 0 and X-RateLimit-Reset
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			isRateLimited = true
			if resetHeader := resp.Header.Get("X-RateLimit-Reset"); resetHeader != "" {
				rateLimitSleep = parseRateLimitReset(resetHeader)
			}
		}

		if isRateLimited {
			resp.Body.Close()
			if attempt == c.maxRetries {
				return nil, NewTransientError("rate limit exceeded, maximum retries reached", resp.StatusCode, nil)
			}
			// Cap sleep duration to avoid hanging requests indefinitely
			if rateLimitSleep > 15*time.Second {
				return nil, NewTransientError(fmt.Sprintf("rate limit exceeded, wait time %s too long", rateLimitSleep), resp.StatusCode, nil)
			}
			if rateLimitSleep == 0 {
				rateLimitSleep = c.calculateBackoff(attempt)
			}
			c.sleep(ctx, attempt, rateLimitSleep)
			continue
		}

		// 5. Handle Other HTTP Codes
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			if attempt == c.maxRetries {
				return nil, NewTransientError(fmt.Sprintf("server error: %d", resp.StatusCode), resp.StatusCode, nil)
			}
			c.sleep(ctx, attempt, 0)
			continue
		}

		// Success or client-side permanent error (4xx besides rate limit / auth)
		if resp.StatusCode >= 400 {
			return resp, NewPermanentError(fmt.Sprintf("request failed with status %d", resp.StatusCode), resp.StatusCode, nil)
		}

		return resp, nil
	}

	return nil, NewTransientError("request failed, max retries reached", 0, nil)
}

func (c *HTTPClient) applyAuth(req *http.Request) {
	// Set target full URL if URL is path-only
	if req.URL.Scheme == "" && c.baseURL != "" {
		baseURL := c.baseURL
		scheme := "http"
		host := baseURL

		if strings.HasPrefix(baseURL, "http://") {
			scheme = "http"
			host = baseURL[7:]
		} else if strings.HasPrefix(baseURL, "https://") {
			scheme = "https"
			host = baseURL[8:]
		}

		req.URL.Scheme = scheme
		req.URL.Host = host
	}

	// Make sure Host matches base host
	if req.URL.Host == "" {
		// Just parse baseURL
		// For simplicity, let's ensure we do this properly
	}

	switch c.authConfig.Type {
	case AuthTypeOAuth2:
		if c.creds.Token != nil {
			req.Header.Set("Authorization", "Bearer "+c.creds.Token.AccessToken)
		}
	case AuthTypeAPIKey:
		if c.authConfig.APIKey != nil && c.creds.APIKey != "" {
			name := c.authConfig.APIKey.HeaderName
			prefix := c.authConfig.APIKey.ValuePrefix
			req.Header.Set(name, prefix+c.creds.APIKey)
		}
	}
}

func (c *HTTPClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff = baseBackoff * 2^attempt
	backoff := c.baseBackoff * time.Duration(1<<attempt)
	if backoff > c.maxBackoff {
		backoff = c.maxBackoff
	}
	// Add jitter (up to 50% random addition)
	jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
	return backoff + jitter
}

func (c *HTTPClient) sleep(ctx context.Context, attempt int, forceDuration time.Duration) {
	duration := forceDuration
	if duration == 0 {
		duration = c.calculateBackoff(attempt)
	}
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}

func parseRetryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func parseRateLimitReset(h string) time.Duration {
	if epoch, err := strconv.ParseInt(h, 10, 64); err == nil {
		t := time.Unix(epoch, 0)
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
