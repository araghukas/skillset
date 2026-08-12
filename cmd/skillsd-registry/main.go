package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/evidence"
	"github.com/araghukas/skillset/internal/evidencetools"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/mcphttp"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/proposaltools"
	"github.com/araghukas/skillset/internal/registryconfig"
	"github.com/araghukas/skillset/internal/submit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is stamped at build time; unset in `go run`/`go test` builds.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("skillsd-registry exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := registryconfig.Load()
	if err != nil {
		return err
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)

	// Unlike skillsd's read-only volume (populated once by an init
	// container), RepoDir is expected to be a persistent volume this
	// process owns: it clones into it on first start, then keeps reopening
	// and mutating the same working copy across restarts.
	repo, err := gitrepo.Open(ctx, cfg.RepoDir, cfg.SkillsRepoURL, cfg.SkillsRepoBaseBranch, cfg.GitHubAuth)
	if err != nil {
		return fmt.Errorf("opening skills repo: %w", err)
	}
	slog.Info("opened skills repo",
		"dir", cfg.RepoDir, "base_branch", cfg.SkillsRepoBaseBranch, "github_auth_mode", cfg.GitHubAuthMode)
	svc := proposals.New(repo, cfg.SkillsSubPath, cfg.MaxFileContentBytes)
	gh := githubpr.New(cfg.GitHubAPIBaseURL, cfg.GitHubOwner, cfg.GitHubRepo, cfg.GitHubAuth)

	// Corroboration is the only thing that opens a pull request, so both the
	// threshold and the credential have to be in place for a proposal to
	// ever leave this pod. Each missing half is worth saying out loud at
	// startup: the alternative is proposals silently accumulating on a
	// volume nobody is watching.
	switch {
	case cfg.AutoSubmitEndorsements <= 0:
		slog.Warn("auto-submission is off: proposals will accumulate as local branches and are never pushed",
			"hint", "set AUTO_SUBMIT_ENDORSEMENTS")
	case !cfg.SubmitConfigured:
		slog.Warn("auto-submission is configured but no pull request can be opened: GitHub credential, owner, or repo is missing",
			"threshold", cfg.AutoSubmitEndorsements)
	default:
		slog.Info("auto-submission is enabled: proposals corroborated by enough agents open pull requests",
			"threshold", cfg.AutoSubmitEndorsements)
	}

	go refreshBaseLoop(ctx, repo, cfg.FetchInterval)

	submitter := submit.New(svc, gh, cfg.SkillsRepoBaseBranch)

	// Evidence collection is optional: without it the registry is still a
	// complete proposal path, just one whose pull requests arrive without
	// the field data that motivated them. The evidence tools are only
	// registered below when a store is opened, so a disabled configuration
	// means those tools are simply absent from tools/list.
	var store *evidence.Store
	if cfg.EvidenceEnabled {
		store, err = openEvidence(ctx, cfg)
		if err != nil {
			return err
		}
		defer store.Close()

		go retentionLoop(ctx, store, cfg.EvidenceRollupInterval, cfg.EvidenceRetention)
		go backupLoop(ctx, store, cfg.EvidenceBackupPath, cfg.EvidenceBackupInterval)
	} else {
		slog.Info("evidence collection is disabled; no outcome reports will be collected")
	}

	guideAppendix := repoConfigSection(cfg)

	srv := mcp.NewServer(
		&mcp.Implementation{Name: "skillsd-registry", Version: version},
		&mcp.ServerOptions{
			Instructions: clientguide.Instructions("registry", guideAppendix),
			// Suppress the SDK's default advertisement of a "logging"
			// capability, which this server does not implement.
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	proposaltools.Add(srv, proposaltools.Deps{
		Proposals:           svc,
		Submitter:           submitter,
		SubmitConfigured:    cfg.SubmitConfigured,
		AutoSubmitThreshold: cfg.AutoSubmitEndorsements,
		DefaultMaxBytes:     cfg.MaxResultBytes,
		ClientGuideAppendix: guideAppendix,
	})
	if store != nil {
		evidencetools.Add(srv, evidencetools.Deps{
			Store:    store,
			Resolver: svc,
			Verify:   cfg.EvidenceVerifyCommits,
		})
	}

	return mcphttp.Serve(ctx, srv, mcphttp.Options{
		Addr:                cfg.HTTPAddr,
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	})
}

// repoConfigSection builds the "Repository configuration" section appended
// to the client guide, naming the two repos/branches a proposal passes
// through - the repo skills are read from and forked from, and the repo
// corroborated proposals open pull requests against. The two are usually the
// same repo, but nothing enforces that (GitHubOwner/GitHubRepo can name a
// different repo than SkillsRepoURL points to), so both are spelled out
// rather than assumed identical.
func repoConfigSection(cfg registryconfig.Config) string {
	prRepo := "not configured - no pull requests are opened"
	if cfg.GitHubOwner != "" && cfg.GitHubRepo != "" {
		prRepo = fmt.Sprintf("https://github.com/%s/%s", cfg.GitHubOwner, cfg.GitHubRepo)
	}
	return fmt.Sprintf(
		"## Repository configuration\n\n"+
			"- Skills are read from, and proposals are forked from, %s on branch %q.\n"+
			"- Corroborated proposals open pull requests against %s, targeting branch %q.\n",
		cfg.SkillsRepoURL, cfg.SkillsRepoBaseBranch,
		prRepo, cfg.SkillsRepoBaseBranch,
	)
}

// openEvidence opens the outcome-report database, creating its parent
// directory if the volume is mounted empty.
func openEvidence(ctx context.Context, cfg registryconfig.Config) (*evidence.Store, error) {
	if dir := filepath.Dir(cfg.EvidenceDBPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating evidence directory %s: %w", dir, err)
		}
	}

	store, err := evidence.Open(ctx, cfg.EvidenceDBPath)
	if err != nil {
		return nil, fmt.Errorf("opening evidence store: %w", err)
	}
	slog.Info("opened evidence store", "path", cfg.EvidenceDBPath,
		"verify_commits", cfg.EvidenceVerifyCommits, "retention", cfg.EvidenceRetention)
	return store, nil
}

// retentionLoop folds reports older than the retention window into
// aggregate counts and deletes the raw rows.
//
// Left unbounded, raw reports accumulate until the volume fills, and the
// failure surfaces as a registry that can no longer accept writes. Rolling
// them up keeps the signal - which is what anyone actually queries - while
// bounding the file.
func retentionLoop(ctx context.Context, store *evidence.Store, interval, retention time.Duration) {
	if retention <= 0 {
		slog.Warn("evidence retention is disabled; the database will grow without bound")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := store.Rollup(ctx, time.Now().Add(-retention))
			if err != nil {
				slog.Error("evidence rollup failed", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("rolled up aged outcome reports", "reports", n, "retention", retention)
			}
		}
	}
}

// backupLoop periodically snapshots the evidence database.
//
// Everything else this component owns is a cache of a git remote and can be
// rebuilt by re-cloning. Outcome reports cannot: they are observations that
// exist in exactly one place. Each run replaces the previous snapshot, so
// the destination should be somewhere that outlives the pod.
func backupLoop(ctx context.Context, store *evidence.Store, path string, interval time.Duration) {
	if path == "" {
		slog.Warn("evidence backups are disabled; outcome reports exist only on this volume and cannot be reconstructed if it is lost",
			"hint", "set EVIDENCE_BACKUP_PATH")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// VACUUM INTO refuses to overwrite, so the previous snapshot is
			// swapped out only once the new one has been written in full -
			// there is never a moment with no usable backup on disk.
			tmp := path + ".tmp"
			_ = os.Remove(tmp)
			if err := store.Backup(ctx, tmp); err != nil {
				slog.Error("evidence backup failed", "error", err)
				continue
			}
			if err := os.Rename(tmp, path); err != nil {
				slog.Error("could not replace previous evidence backup", "error", err)
				continue
			}
			slog.Info("wrote evidence backup", "path", path)
		}
	}
}

// refreshBaseLoop periodically re-fetches the base branch from origin so
// reads stay fresh without a network round-trip per request. Unlike
// skillsd's static, once-loaded index, this component is meant to track
// live upstream state for as long as the pod runs.
func refreshBaseLoop(ctx context.Context, repo *gitrepo.Repo, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := repo.RefreshBase(ctx); err != nil {
				slog.Error("refreshing base branch failed", "error", err)
			}
		}
	}
}
