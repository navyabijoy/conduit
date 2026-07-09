package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"conduit/sdk"
)

type GitHubConnector struct {
	mockBaseURL string
}

func NewGitHubConnector(mockBaseURL string) *GitHubConnector {
	if mockBaseURL == "" {
		mockBaseURL = "localhost:8081"
	}
	return &GitHubConnector{mockBaseURL: mockBaseURL}
}

type CreateIssueInput struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type GitHubIssue struct {
	ID     int    `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (c *GitHubConnector) Metadata() sdk.ConnectorMetadata {
	return sdk.ConnectorMetadata{
		ID:          "github",
		Name:        "GitHub",
		Description: "Create issues and list issue history in repositories.",
		Category:    "Developer Tools",
		Scopes:      []string{"repo", "user"},
	}
}

func (c *GitHubConnector) AuthConfig() sdk.AuthConfig {
	return sdk.AuthConfig{
		Type: sdk.AuthTypeOAuth2,
		OAuth2: &sdk.OAuth2Config{
			AuthURL:  fmt.Sprintf("http://%s/mock/oauth/authorize", c.mockBaseURL),
			TokenURL: fmt.Sprintf("http://%s/services/github/login/oauth/access_token", c.mockBaseURL),
			Scopes:   []string{"repo", "user"},
		},
	}
}

func (c *GitHubConnector) Endpoints() []sdk.EndpointDefinition {
	return []sdk.EndpointDefinition{
		{
			Name:         "create_issue",
			Method:       "POST",
			Path:         "/services/github/repos/{owner}/{repo}/issues",
			Description:  "Create a new issue in a GitHub repository.",
			InputSchema:  &CreateIssueInput{},
			OutputSchema: &GitHubIssue{},
			Execute:      c.executeCreateIssue,
		},
		{
			Name:         "list_issues",
			Method:       "GET",
			Path:         "/services/github/repos/{owner}/{repo}/issues",
			Description:  "List issues for a GitHub repository.",
			InputSchema:  nil, // No body input schema needed for GET
			OutputSchema: &[]GitHubIssue{},
			Execute:      c.executeListIssues,
			GetPaginator: c.getPaginatorForListIssues,
		},
	}
}

func (c *GitHubConnector) executeCreateIssue(ctx context.Context, client *sdk.HTTPClient, req sdk.Request) (sdk.Response, error) {
	var input CreateIssueInput
	if err := json.Unmarshal(req.Body, &input); err != nil {
		return sdk.Response{}, sdk.NewPermanentError("invalid JSON body", http.StatusBadRequest, err)
	}

	owner := req.Params["owner"]
	repo := req.Params["repo"]
	if owner == "" {
		owner = input.Owner
	}
	if repo == "" {
		repo = input.Repo
	}

	if owner == "" || repo == "" || input.Title == "" {
		return sdk.Response{}, sdk.NewPermanentError("owner, repo, and title are required", http.StatusBadRequest, nil)
	}

	path := fmt.Sprintf("/services/github/repos/%s/%s/issues", owner, repo)

	payloadBytes, err := json.Marshal(map[string]string{
		"title": input.Title,
		"body":  input.Body,
	})
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to marshal request", http.StatusInternalServerError, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", path, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to create HTTP request", http.StatusInternalServerError, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

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

func (c *GitHubConnector) executeListIssues(ctx context.Context, client *sdk.HTTPClient, req sdk.Request) (sdk.Response, error) {
	owner := req.Params["owner"]
	repo := req.Params["repo"]
	if owner == "" || repo == "" {
		return sdk.Response{}, sdk.NewPermanentError("owner and repo are required params", http.StatusBadRequest, nil)
	}

	path := fmt.Sprintf("/services/github/repos/%s/%s/issues", owner, repo)
	
	// Add query params if any
	reqURL, err := url.Parse(path)
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("invalid path", http.StatusInternalServerError, err)
	}
	q := reqURL.Query()
	for k, v := range req.Params {
		if k != "owner" && k != "repo" {
			q.Set(k, v)
		}
	}
	reqURL.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", reqURL.String(), nil)
	if err != nil {
		return sdk.Response{}, sdk.NewPermanentError("failed to create HTTP request", http.StatusInternalServerError, err)
	}

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

func (c *GitHubConnector) getPaginatorForListIssues(path string, params map[string]string, limit int) sdk.Paginator {
	owner := params["owner"]
	repo := params["repo"]
	resolvedPath := fmt.Sprintf("/services/github/repos/%s/%s/issues", owner, repo)
	
	// Build query params
	reqURL, _ := url.Parse(resolvedPath)
	q := reqURL.Query()
	for k, v := range params {
		if k != "owner" && k != "repo" && k != "cursor" && k != "limit" {
			q.Set(k, v)
		}
	}
	reqURL.RawQuery = q.Encode()

	return sdk.NewCursorPaginator(
		reqURL.String(),
		"cursor",
		"limit",
		limit,
		extractGitHubNextLink,
	)
}

func extractGitHubNextLink(body []byte, headers http.Header) (string, bool, error) {
	linkHeader := headers.Get("Link")
	if linkHeader == "" {
		return "", false, nil
	}

	links := strings.Split(linkHeader, ",")
	for _, link := range links {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(parts[0])
		relPart := strings.TrimSpace(parts[1])

		if strings.Contains(relPart, `rel="next"`) {
			if len(urlPart) > 2 && urlPart[0] == '<' && urlPart[len(urlPart)-1] == '>' {
				nextURL := urlPart[1 : len(urlPart)-1]
				u, err := url.Parse(nextURL)
				if err != nil {
					return "", false, err
				}
				// Return full request URI containing path and query
				return u.RequestURI(), true, nil
			}
		}
	}
	return "", false, nil
}

func (c *GitHubConnector) HandleWebhook(ctx context.Context, event sdk.WebhookEvent) error {
	sig := event.Headers["X-Hub-Signature-256"]
	if sig == "" {
		return sdk.NewPermanentError("missing GitHub signature header", http.StatusBadRequest, nil)
	}

	// Signatures usually look like sha256=hex...
	secret := []byte("mock_webhook_secret_value")

	if !sdk.VerifyHMACSHA256HexSignature(event.Payload, sig, "sha256=", secret) {
		return sdk.NewPermanentError("invalid webhook signature", http.StatusUnauthorized, nil)
	}

	return nil
}
