// Package registryconfig loads skillsd-registry's runtime configuration
// from environment variables. It's the write-path counterpart to
// internal/config, which configures the read-only skillsd binary.
package registryconfig

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/araghukas/skillset/internal/githubauth"
	"github.com/araghukas/skillset/internal/suggestions"
)

// defaultMaxRequestBodyBytes and defaultMaxResultBytes apply when their
// respective env vars are unset. See internal/config for the reasoning
// behind each default - this binary mirrors it.
const (
	defaultMaxRequestBodyBytes = 8 << 20   // 8 MiB
	defaultMaxResultBytes      = 256 << 10 // 256 KiB
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	// HTTPAddr is the address the MCP server listens on over Streamable
	// HTTP.
	HTTPAddr string

	// MaxRequestBodyBytes caps a single incoming MCP request body - the
	// whole record_suggestion call, including every file.
	MaxRequestBodyBytes int64

	// MaxResultBytes caps the context-file content one get_skill_at_ref
	// call returns, and the diff one get_suggestion call returns. Not
	// transport-enforced; internal/toolresult applies it when building a
	// reply, dropping whole files or truncating at a diff hunk boundary
	// and always naming what was left out.
	MaxResultBytes int

	// RepoDir is the local directory the git working copy is kept in. It's
	// expected to be a persistent volume: unlike skillsd's read-only
	// snapshot, this directory's contents must survive pod restarts.
	RepoDir string

	// SkillsRepoURL is the HTTPS clone URL of the skills repository.
	SkillsRepoURL string

	// SkillsRepoBaseBranch is the branch suggestions fork from and pull
	// requests target.
	SkillsRepoBaseBranch string

	// SkillsSubPath is an optional subdirectory within the repo under
	// which skill directories actually live, matching internal/config's
	// SkillsSubPath.
	SkillsSubPath string

	// GitHubAuth supplies the credential for both the HTTPS git clone/
	// fetch/push and the GitHub REST API calls used to open pull requests -
	// one credential rather than two. Nil means none was configured, which
	// leaves auto-submission unable to open pull requests (see
	// SubmitConfigured below) and limits the repo to public, unauthenticated
	// access.
	GitHubAuth githubauth.TokenSource

	// GitHubAuthMode records which scheme GitHubAuth came from, for
	// startup logging. It is githubauth.ModeNone whenever GitHubAuth is
	// nil.
	GitHubAuthMode githubauth.Mode

	// GitHubOwner and GitHubRepo identify the repository pull requests are
	// opened against.
	GitHubOwner string
	GitHubRepo  string

	// GitHubAPIBaseURL overrides the GitHub API base URL, for GitHub
	// Enterprise deployments.
	GitHubAPIBaseURL string

	// SubmitConfigured reports whether pushing a branch and opening a pull
	// request is possible at all: a credential, an owner, and a repo. It is
	// derived from those three rather than set on its own, since there is no
	// useful configuration where they are present and submission is not
	// wanted - AutoSubmitEndorsements is where that choice is made.
	SubmitConfigured bool

	// AutoSubmitEndorsements is how many agents must stand behind a
	// suggestion's current content - its author plus the agents that read
	// the diff and endorsed it as-is - before a pull request is opened for
	// it. It is the only path to a pull request: no tool lets a caller ask
	// for one directly. Zero means suggestions accumulate as local branches
	// and are never pushed anywhere.
	//
	// The threshold is exactly as trustworthy as agent_id is: with
	// self-asserted identities, one misbehaving caller can manufacture a
	// threshold's worth of agreement by itself. Endorsement makes this more
	// load-bearing than it used to be - an endorsement is one agent's
	// judgment, not a hash match - so size it for callers you have
	// authenticated.
	AutoSubmitEndorsements int

	// FetchInterval is how often the base branch is re-fetched from
	// origin in the background.
	FetchInterval time.Duration

	// EvidenceEnabled controls whether the evidence tools are registered
	// at all. Disabled, no database is opened and report_outcome,
	// list_skill_signals, and list_outcome_reports are simply absent from
	// tools/list; everything else about the registry is unaffected.
	EvidenceEnabled bool

	// EvidenceDBPath is the SQLite file outcome reports are stored in.
	// Unlike RepoDir, whose contents are a cache of a git remote, nothing
	// upstream can reconstruct this file - see internal/evidence.
	EvidenceDBPath string

	// EvidenceVerifyCommits makes report_outcome reject reports naming a
	// skill/commit pair the repository doesn't contain.
	EvidenceVerifyCommits bool

	// EvidenceRetention is how long raw reports are kept before being
	// folded into aggregate counts and deleted. Zero disables the rollup,
	// which lets the database grow without bound - only reasonable for a
	// short-lived test deployment.
	EvidenceRetention time.Duration

	// EvidenceRollupInterval is how often the retention pass runs.
	EvidenceRollupInterval time.Duration

	// EvidenceBackupPath is where periodic snapshots of the evidence
	// database are written. Empty disables snapshots.
	EvidenceBackupPath string

	// EvidenceBackupInterval is how often a snapshot is taken.
	EvidenceBackupInterval time.Duration

	// MaxFileContentBytes caps a single FileEdit's content, passed
	// through to suggestions.New.
	MaxFileContentBytes int

	// LogLevel is the minimum slog level emitted by the process.
	LogLevel slog.Level
}

// Load reads configuration from environment variables, applying defaults
// where possible.
func Load() (Config, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("parsing LOG_LEVEL: %w", err)
	}

	fetchInterval, err := time.ParseDuration(getenv("FETCH_INTERVAL", "5m"))
	if err != nil {
		return Config{}, fmt.Errorf("parsing FETCH_INTERVAL: %w", err)
	}

	maxFileContentBytes, err := getenvInt("MAX_FILE_CONTENT_BYTES", suggestions.DefaultMaxFileContentBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_FILE_CONTENT_BYTES: %w", err)
	}
	maxRequestBodyBytes, err := getenvInt("MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodyBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_REQUEST_BODY_BYTES: %w", err)
	}
	maxResultBytes, err := getenvInt("MAX_RESULT_BYTES", defaultMaxResultBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_RESULT_BYTES: %w", err)
	}
	autoSubmitEndorsements, err := getenvInt("AUTO_SUBMIT_ENDORSEMENTS", 0)
	if err != nil {
		return Config{}, fmt.Errorf("parsing AUTO_SUBMIT_ENDORSEMENTS: %w", err)
	}
	evidenceEnabled, err := getenvBool("EVIDENCE_ENABLED", true)
	if err != nil {
		return Config{}, fmt.Errorf("parsing EVIDENCE_ENABLED: %w", err)
	}
	evidenceVerifyCommits, err := getenvBool("EVIDENCE_VERIFY_COMMITS", true)
	if err != nil {
		return Config{}, fmt.Errorf("parsing EVIDENCE_VERIFY_COMMITS: %w", err)
	}
	evidenceRetention, err := time.ParseDuration(getenv("EVIDENCE_RETENTION", "2160h")) // 90 days
	if err != nil {
		return Config{}, fmt.Errorf("parsing EVIDENCE_RETENTION: %w", err)
	}
	evidenceRollupInterval, err := time.ParseDuration(getenv("EVIDENCE_ROLLUP_INTERVAL", "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("parsing EVIDENCE_ROLLUP_INTERVAL: %w", err)
	}
	evidenceBackupInterval, err := time.ParseDuration(getenv("EVIDENCE_BACKUP_INTERVAL", "24h"))
	if err != nil {
		return Config{}, fmt.Errorf("parsing EVIDENCE_BACKUP_INTERVAL: %w", err)
	}

	githubAuth, githubAuthMode, err := githubauth.LoadFromEnv()
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:               getenv("HTTP_ADDR", ":8081"),
		MaxRequestBodyBytes:    int64(maxRequestBodyBytes),
		MaxResultBytes:         maxResultBytes,
		RepoDir:                getenv("REPO_DIR", "/var/lib/skillsd-registry"),
		SkillsRepoURL:          getenv("SKILLS_REPO_URL", ""),
		SkillsRepoBaseBranch:   getenv("SKILLS_REPO_BASE_BRANCH", "main"),
		SkillsSubPath:          getenv("SKILLS_SUBPATH", ""),
		GitHubAuth:             githubAuth,
		GitHubAuthMode:         githubAuthMode,
		GitHubOwner:            getenv("GITHUB_OWNER", ""),
		GitHubRepo:             getenv("GITHUB_REPO", ""),
		GitHubAPIBaseURL:       getenv("GITHUB_API_BASE_URL", "https://api.github.com"),
		AutoSubmitEndorsements: autoSubmitEndorsements,
		FetchInterval:          fetchInterval,
		EvidenceEnabled:        evidenceEnabled,
		EvidenceDBPath:         getenv("EVIDENCE_DB_PATH", "/var/lib/skillsd-evidence/evidence.db"),
		EvidenceVerifyCommits:  evidenceVerifyCommits,
		EvidenceRetention:      evidenceRetention,
		EvidenceRollupInterval: evidenceRollupInterval,
		EvidenceBackupPath:     getenv("EVIDENCE_BACKUP_PATH", ""),
		EvidenceBackupInterval: evidenceBackupInterval,
		MaxFileContentBytes:    maxFileContentBytes,
		LogLevel:               level,
	}

	if cfg.SkillsRepoURL == "" {
		return Config{}, fmt.Errorf("SKILLS_REPO_URL is required")
	}

	// GitHub auth is only required for pushing a branch and opening a pull
	// request, not for the rest of the service. Rather than fail to start
	// when it's absent, run without a path to the forge.
	cfg.SubmitConfigured = cfg.GitHubAuth != nil && cfg.GitHubOwner != "" && cfg.GitHubRepo != ""

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.Atoi(v)
}

func getenvBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.ParseBool(v)
}
