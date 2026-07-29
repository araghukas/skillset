// Package githubpr opens GitHub pull requests via the REST API. It's
// deliberately minimal - a single endpoint - rather than pulling in a full
// GitHub SDK, matching this project's otherwise very small dependency set.
package githubpr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client creates pull requests against a single GitHub (or GitHub
// Enterprise) repository.
type Client struct {
	httpClient *http.Client
	baseURL    string
	owner      string
	repo       string
	token      string
}

// New returns a Client for owner/repo. baseURL defaults to
// https://api.github.com when empty; override for GitHub Enterprise. token
// is used as a bearer token and must have pull-request write access.
func New(baseURL, owner, repo, token string) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		httpClient: http.DefaultClient,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		owner:      owner,
		repo:       repo,
		token:      token,
	}
}

// PullRequestInput describes a pull request to open.
type PullRequestInput struct {
	Title string
	Body  string
	Head  string // Branch name; must already exist on the remote.
	Base  string // Branch the PR targets.
}

// PullRequest is the subset of GitHub's response this client cares about.
type PullRequest struct {
	URL    string
	Number int64
}

// CreatePullRequest opens a pull request via
// POST /repos/{owner}/{repo}/pulls.
func (c *Client) CreatePullRequest(ctx context.Context, in PullRequestInput) (*PullRequest, error) {
	body, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body,omitempty"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}{Title: in.Title, Body: in.Body, Head: in.Head, Base: in.Base})
	if err != nil {
		return nil, fmt.Errorf("githubpr: encoding request: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/pulls", c.baseURL, c.owner, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("githubpr: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("githubpr: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("githubpr: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("githubpr: creating pull request: %s: %s", resp.Status, respBody)
	}

	var parsed struct {
		HTMLURL string `json:"html_url"`
		Number  int64  `json:"number"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("githubpr: decoding response: %w", err)
	}

	return &PullRequest{URL: parsed.HTMLURL, Number: parsed.Number}, nil
}
