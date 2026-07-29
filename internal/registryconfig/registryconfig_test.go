package registryconfig

import "testing"

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SKILLS_REPO_URL", "https://github.com/acme/skills.git")
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_OWNER", "acme")
	t.Setenv("GITHUB_REPO", "skills")
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
}

func TestLoadDisablesSubmitProposalWhenGitHubTokenMissing(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GITHUB_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SubmitProposalEnabled {
		t.Fatal("expected SubmitProposalEnabled to be false when GITHUB_TOKEN is unset")
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
