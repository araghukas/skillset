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
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	// GRPCAddr is the address the ProposalService gRPC server listens on.
	GRPCAddr string

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

	// GitHubToken authenticates both the HTTPS git push and the GitHub
	// REST API calls used to open pull requests.
	GitHubToken string

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
	// AND-ed with whether GitHub auth (token/owner/repo) is actually
	// configured.
	SubmitProposalEnabled bool

	// FetchInterval is how often the base branch is re-fetched from
	// origin in the background.
	FetchInterval time.Duration

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
	submitProposalRequested, err := getenvBool("SUBMIT_PROPOSAL_ENABLED", true)
	if err != nil {
		return Config{}, fmt.Errorf("parsing SUBMIT_PROPOSAL_ENABLED: %w", err)
	}

	cfg := Config{
		GRPCAddr:                getenv("GRPC_ADDR", ":8081"),
		RepoDir:                 getenv("REPO_DIR", "/var/lib/skillsd-registry"),
		SkillsRepoURL:           getenv("SKILLS_REPO_URL", ""),
		SkillsRepoBaseBranch:    getenv("SKILLS_REPO_BASE_BRANCH", "main"),
		SkillsSubPath:           getenv("SKILLS_SUBPATH", ""),
		GitHubToken:             getenv("GITHUB_TOKEN", ""),
		GitHubOwner:             getenv("GITHUB_OWNER", ""),
		GitHubRepo:              getenv("GITHUB_REPO", ""),
		GitHubAPIBaseURL:        getenv("GITHUB_API_BASE_URL", "https://api.github.com"),
		FetchInterval:           fetchInterval,
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
		cfg.GitHubToken != "" && cfg.GitHubOwner != "" && cfg.GitHubRepo != ""

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
