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
	"github.com/araghukas/skillset/internal/proposals"
)

// defaultGRPCMaxRecvMsgSizeBytes and defaultGRPCMaxSendMsgSizeBytes apply
// when their respective env vars are unset. gRPC-Go itself defaults both to
// 4 MiB; these are raised a little so a handful of files in one
// ProposeChange call, or a large GetProposal diff, don't run into the
// transport limit, while still bounding memory per request/response rather
// than leaving it unbounded.
const (
	defaultGRPCMaxRecvMsgSizeBytes = 8 << 20 // 8 MiB
	defaultGRPCMaxSendMsgSizeBytes = 8 << 20 // 8 MiB

	// defaultMaxRequestBodyBytes applies when MCP_MAX_REQUEST_BODY_BYTES is
	// unset. It bounds one incoming MCP request body - the whole
	// propose_change call, including every file. The SDK's own default is
	// 4 MiB; this is raised for the same reason the gRPC default was.
	defaultMaxRequestBodyBytes = 8 << 20 // 8 MiB
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	// GRPCAddr is the address the ProposalService gRPC server listens on.
	GRPCAddr string

	// MCPAddr is the address the MCP server listens on over Streamable
	// HTTP. Empty (the default) skips MCP serving entirely. Both this and
	// GRPCAddr can be set at once during the migration to MCP;
	// skillsd-registry runs both listeners against the same underlying
	// services.
	MCPAddr string

	// MaxRequestBodyBytes caps a single incoming MCP request body.
	MaxRequestBodyBytes int64

	// RepoDir is the local directory the git working copy is kept in. It's
	// expected to be a persistent volume: unlike skillsd's read-only
	// snapshot, this directory's contents must survive pod restarts.
	RepoDir string

	// SkillsRepoURL is the HTTPS clone URL of the skills repository.
	SkillsRepoURL string

	// SkillsRepoBaseBranch is the branch proposals fork from and pull
	// requests target.
	SkillsRepoBaseBranch string

	// SkillsSubPath is an optional subdirectory within the repo under
	// which skill directories actually live, matching internal/config's
	// SkillsSubPath.
	SkillsSubPath string

	// GitHubAuth supplies the credential for both the HTTPS git clone/
	// fetch/push and the GitHub REST API calls used to open pull requests -
	// one credential rather than two. Nil means none was configured, which
	// disables SubmitProposal (see below) and limits the repo to public,
	// unauthenticated access.
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

	// SubmitProposalEnabled controls whether the ProposalService's
	// SubmitProposal RPC is allowed to push branches and open pull
	// requests. It's the SUBMIT_PROPOSAL_ENABLED env var (default true)
	// AND-ed with whether GitHub auth (a credential, plus owner/repo) is
	// actually configured.
	SubmitProposalEnabled bool

	// AutoSubmitEndorsements is how many agents must independently arrive
	// at identical content before a pull request is opened for it without
	// anyone asking. Zero (the default) disables auto-submission entirely.
	//
	// This is the only setting that lets the registry act on its own, and
	// it is exactly as trustworthy as agent_id is: with self-asserted
	// identities, one misbehaving caller can manufacture a threshold's
	// worth of agreement by itself. Enable it once callers are
	// authenticated, not before.
	AutoSubmitEndorsements int

	// FetchInterval is how often the base branch is re-fetched from
	// origin in the background.
	FetchInterval time.Duration

	// EvidenceEnabled controls whether EvidenceService is served at all.
	// Disabled, its RPCs return Unimplemented and no database is opened;
	// everything else about the registry is unaffected.
	EvidenceEnabled bool

	// EvidenceDBPath is the SQLite file outcome reports are stored in.
	// Unlike RepoDir, whose contents are a cache of a git remote, nothing
	// upstream can reconstruct this file - see internal/evidence.
	EvidenceDBPath string

	// EvidenceVerifyCommits makes ReportOutcome reject reports naming a
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

	// GRPCMaxRecvMsgSizeBytes caps the size of a single incoming gRPC
	// message (grpc.MaxRecvMsgSize) - the whole ProposeChangeRequest,
	// including every FileChange in the call.
	GRPCMaxRecvMsgSizeBytes int

	// GRPCMaxSendMsgSizeBytes caps the size of a single outgoing gRPC
	// message (grpc.MaxSendMsgSize) - relevant here for GetProposal's diff
	// and GetSkillAtRef's context files.
	GRPCMaxSendMsgSizeBytes int

	// MaxFileContentBytes caps a single FileChange's content, passed
	// through to proposals.New.
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

	maxRecvMsgSize, err := getenvInt("GRPC_MAX_RECV_MSG_SIZE_BYTES", defaultGRPCMaxRecvMsgSizeBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing GRPC_MAX_RECV_MSG_SIZE_BYTES: %w", err)
	}
	maxSendMsgSize, err := getenvInt("GRPC_MAX_SEND_MSG_SIZE_BYTES", defaultGRPCMaxSendMsgSizeBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing GRPC_MAX_SEND_MSG_SIZE_BYTES: %w", err)
	}
	maxFileContentBytes, err := getenvInt("MAX_FILE_CONTENT_BYTES", proposals.DefaultMaxFileContentBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_FILE_CONTENT_BYTES: %w", err)
	}
	maxRequestBodyBytes, err := getenvInt("MCP_MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodyBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MCP_MAX_REQUEST_BODY_BYTES: %w", err)
	}
	submitProposalRequested, err := getenvBool("SUBMIT_PROPOSAL_ENABLED", true)
	if err != nil {
		return Config{}, fmt.Errorf("parsing SUBMIT_PROPOSAL_ENABLED: %w", err)
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
		GRPCAddr:                getenv("GRPC_ADDR", ":8081"),
		MCPAddr:                 getenv("MCP_ADDR", ""),
		MaxRequestBodyBytes:     int64(maxRequestBodyBytes),
		RepoDir:                 getenv("REPO_DIR", "/var/lib/skillsd-registry"),
		SkillsRepoURL:           getenv("SKILLS_REPO_URL", ""),
		SkillsRepoBaseBranch:    getenv("SKILLS_REPO_BASE_BRANCH", "main"),
		SkillsSubPath:           getenv("SKILLS_SUBPATH", ""),
		GitHubAuth:              githubAuth,
		GitHubAuthMode:          githubAuthMode,
		GitHubOwner:             getenv("GITHUB_OWNER", ""),
		GitHubRepo:              getenv("GITHUB_REPO", ""),
		GitHubAPIBaseURL:        getenv("GITHUB_API_BASE_URL", "https://api.github.com"),
		AutoSubmitEndorsements:  autoSubmitEndorsements,
		FetchInterval:           fetchInterval,
		EvidenceEnabled:         evidenceEnabled,
		EvidenceDBPath:          getenv("EVIDENCE_DB_PATH", "/var/lib/skillsd-evidence/evidence.db"),
		EvidenceVerifyCommits:   evidenceVerifyCommits,
		EvidenceRetention:       evidenceRetention,
		EvidenceRollupInterval:  evidenceRollupInterval,
		EvidenceBackupPath:      getenv("EVIDENCE_BACKUP_PATH", ""),
		EvidenceBackupInterval:  evidenceBackupInterval,
		GRPCMaxRecvMsgSizeBytes: maxRecvMsgSize,
		GRPCMaxSendMsgSizeBytes: maxSendMsgSize,
		MaxFileContentBytes:     maxFileContentBytes,
		LogLevel:                level,
	}

	if cfg.SkillsRepoURL == "" {
		return Config{}, fmt.Errorf("SKILLS_REPO_URL is required")
	}

	// GitHub auth is only required for SubmitProposal (pushing + opening a
	// PR), not for the rest of the service. Rather than fail to start when
	// it's absent, disable SubmitProposal.
	cfg.SubmitProposalEnabled = submitProposalRequested &&
		cfg.GitHubAuth != nil && cfg.GitHubOwner != "" && cfg.GitHubRepo != ""

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
