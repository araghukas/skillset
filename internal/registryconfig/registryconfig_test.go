package registryconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/araghukas/skillset/internal/githubauth"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	// Clear the app vars too: a developer's own GITHUB_APP_* would
	// otherwise leak into the token-mode cases below.
	for _, key := range []string{
		"GITHUB_AUTH_MODE",
		"GITHUB_APP_ID",
		"GITHUB_APP_INSTALLATION_ID",
		"GITHUB_APP_PRIVATE_KEY_PATH",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("SKILLS_REPO_URL", "https://github.com/acme/skills.git")
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_OWNER", "acme")
	t.Setenv("GITHUB_REPO", "skills")
}

// setAppEnv switches the required env from token auth to a GitHub App
// installation, writing a throwaway private key for it to load.
func setAppEnv(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-key.pem")
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_AUTH_MODE", string(githubauth.ModeGitHubApp))
	t.Setenv("GITHUB_APP_ID", "Iv23abc")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "987")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", path)
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCAddr != ":8081" {
		t.Errorf("unexpected GRPCAddr: %q", cfg.GRPCAddr)
	}
	if cfg.SkillsRepoBaseBranch != "main" {
		t.Errorf("unexpected SkillsRepoBaseBranch: %q", cfg.SkillsRepoBaseBranch)
	}
	if cfg.GitHubAPIBaseURL != "https://api.github.com" {
		t.Errorf("unexpected GitHubAPIBaseURL: %q", cfg.GitHubAPIBaseURL)
	}
	if cfg.FetchInterval.String() != "5m0s" {
		t.Errorf("unexpected FetchInterval: %v", cfg.FetchInterval)
	}
}

func TestLoadRequiresSkillsRepoURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SKILLS_REPO_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected error when SKILLS_REPO_URL is unset")
	}
}

func TestLoadEnablesSubmitProposalWhenGitHubAuthPresent(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SubmitProposalEnabled {
		t.Fatal("expected SubmitProposalEnabled to be true when GitHub auth is fully configured")
	}
	if cfg.GitHubAuthMode != githubauth.ModeToken {
		t.Errorf("expected mode %s, got %s", githubauth.ModeToken, cfg.GitHubAuthMode)
	}
}

func TestLoadEnablesSubmitProposalWithGitHubApp(t *testing.T) {
	setRequiredEnv(t)
	setAppEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SubmitProposalEnabled {
		t.Fatal("expected SubmitProposalEnabled to be true when a GitHub App is configured")
	}
	if cfg.GitHubAuthMode != githubauth.ModeGitHubApp {
		t.Errorf("expected mode %s, got %s", githubauth.ModeGitHubApp, cfg.GitHubAuthMode)
	}
	if cfg.GitHubAuth == nil {
		t.Error("expected a TokenSource for the configured app")
	}
}

func TestLoadFailsOnIncompleteGitHubApp(t *testing.T) {
	// A half-configured app is always a mistake. Unlike a missing token it
	// fails startup outright rather than silently degrading to
	// propose-only mode, which would look like a working deployment.
	setRequiredEnv(t)
	setAppEnv(t)
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when the GitHub App config is incomplete")
	}
}

func TestLoadDisablesSubmitProposalWhenGitHubAuthMissing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITHUB_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubmitProposalEnabled {
		t.Fatal("expected SubmitProposalEnabled to be false when no credential is configured")
	}
	if cfg.GitHubAuth != nil {
		t.Errorf("expected a nil TokenSource, got %#v", cfg.GitHubAuth)
	}
}

func TestLoadDisablesSubmitProposalWhenExplicitlyDisabled(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SUBMIT_PROPOSAL_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubmitProposalEnabled {
		t.Fatal("expected SubmitProposalEnabled to be false when SUBMIT_PROPOSAL_ENABLED=false")
	}
}
