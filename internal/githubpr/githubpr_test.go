package githubpr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

	client := New(server.URL, "acme", "skills", "test-token")
	pr, err := client.CreatePullRequest(context.Background(), PullRequestInput{
		Title: "Fix typo in frontend-design",
		Body:  "proposed by agent-1",
		Head:  "proposals/agent-1/frontend-design/fix-typo",
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
	if gotBody["head"] != "proposals/agent-1/frontend-design/fix-typo" || gotBody["base"] != "main" {
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

	client := New(server.URL, "acme", "skills", "test-token")
	_, err := client.CreatePullRequest(context.Background(), PullRequestInput{
		Title: "x", Head: "proposals/agent-1/frontend-design/fix-typo", Base: "main",
	})
	if err == nil {
		t.Fatal("expected error for non-201 response")
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	client := New("", "acme", "skills", "token")
	if client.baseURL != "https://api.github.com" {
		t.Fatalf("expected default baseURL, got %q", client.baseURL)
	}
}
