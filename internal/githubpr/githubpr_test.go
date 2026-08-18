package githubpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/araghukas/skillset/internal/githubauth"
)

func TestCreatePullRequestSendsExpectedRequest(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url": "https://github.com/acme/skills/pull/42", "number": 42}`))
	}))
	defer server.Close()

	client := New(server.URL, "acme", "skills", githubauth.Static("test-token"))
	pr, err := client.CreatePullRequest(context.Background(), PullRequestInput{
		Title: "Fix typo in frontend-design",
		Body:  "suggested by agent-1",
		Head:  "suggestions/agent-1/frontend-design/fix-typo",
		Base:  "main",
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/repos/acme/skills/pulls" {
		t.Errorf("unexpected path: %s", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("unexpected Authorization header: %s", gotAuth)
	}
	if gotBody["head"] != "suggestions/agent-1/frontend-design/fix-typo" || gotBody["base"] != "main" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}

	if pr.URL != "https://github.com/acme/skills/pull/42" || pr.Number != 42 {
		t.Errorf("unexpected pull request: %+v", pr)
	}
}

func TestCreatePullRequestReturnsErrorOnNon201(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message": "Validation Failed"}`))
	}))
	defer server.Close()

	client := New(server.URL, "acme", "skills", githubauth.Static("test-token"))
	_, err := client.CreatePullRequest(context.Background(), PullRequestInput{
		Title: "x", Head: "suggestions/agent-1/frontend-design/fix-typo", Base: "main",
	})
	if err == nil {
		t.Fatal("expected error for non-201 response")
	}
}

// rotatingTokens hands out a different token each time, standing in for a
// GitHub App installation token that expired between two calls.
type rotatingTokens struct{ calls int }

func (r *rotatingTokens) Token(context.Context) (string, error) {
	r.calls++
	return fmt.Sprintf("ghs_token_%d", r.calls), nil
}

func TestCreatePullRequestReadsTheTokenPerRequest(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"html_url": "https://github.com/acme/skills/pull/1", "number": 1}`))
	}))
	defer server.Close()

	client := New(server.URL, "acme", "skills", &rotatingTokens{})
	for range 2 {
		if _, err := client.CreatePullRequest(context.Background(), PullRequestInput{
			Title: "x", Head: "suggestions/agent-1/frontend-design/fix-typo", Base: "main",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A client that cached the token at construction would send the same
	// header twice - and, once an installation token lapsed, keep sending a
	// dead one.
	if gotAuth[0] != "Bearer ghs_token_1" || gotAuth[1] != "Bearer ghs_token_2" {
		t.Errorf("expected a freshly read token per request, got %q then %q", gotAuth[0], gotAuth[1])
	}
}

func TestCreatePullRequestFailsWithoutACredential(t *testing.T) {
	client := New("https://api.github.com", "acme", "skills", nil)
	_, err := client.CreatePullRequest(context.Background(), PullRequestInput{
		Title: "x", Head: "suggestions/agent-1/frontend-design/fix-typo", Base: "main",
	})
	if err == nil {
		t.Fatal("expected an error when no credential is configured")
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	client := New("", "acme", "skills", githubauth.Static("token"))
	if client.baseURL != "https://api.github.com" {
		t.Fatalf("expected default baseURL, got %q", client.baseURL)
	}
}
