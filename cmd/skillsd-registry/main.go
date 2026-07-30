package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/evidence"
	"github.com/araghukas/skillset/internal/evidenceserver"
	"github.com/araghukas/skillset/internal/githubpr"
	"github.com/araghukas/skillset/internal/gitrepo"
	"github.com/araghukas/skillset/internal/proposals"
	"github.com/araghukas/skillset/internal/proposalserver"
	"github.com/araghukas/skillset/internal/registryconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

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
	repo, err := gitrepo.Open(ctx, cfg.RepoDir, cfg.SkillsRepoURL, cfg.SkillsRepoBaseBranch, cfg.GitHubToken)
	if err != nil {
		return fmt.Errorf("opening skills repo: %w", err)
	}
	slog.Info("opened skills repo", "dir", cfg.RepoDir, "base_branch", cfg.SkillsRepoBaseBranch)
	if !cfg.SubmitProposalEnabled {
		slog.Warn("SubmitProposal is disabled: GitHub auth not configured or SUBMIT_PROPOSAL_ENABLED=false")
	}

	svc := proposals.New(repo, cfg.SkillsSubPath, cfg.MaxFileContentBytes)
	gh := githubpr.New(cfg.GitHubAPIBaseURL, cfg.GitHubOwner, cfg.GitHubRepo, cfg.GitHubToken)

	if cfg.AutoSubmitEndorsements > 0 {
		slog.Warn("auto-submission is enabled: proposals corroborated by enough agents will open pull requests unprompted",
			"threshold", cfg.AutoSubmitEndorsements)
	}

	go refreshBaseLoop(ctx, repo, cfg.FetchInterval)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(cfg.GRPCMaxRecvMsgSizeBytes),
		grpc.MaxSendMsgSize(cfg.GRPCMaxSendMsgSizeBytes),
	)
	skillsv1.RegisterProposalServiceServer(grpcServer, proposalserver.New(
		svc, gh, cfg.SkillsRepoBaseBranch, cfg.SubmitProposalEnabled, cfg.AutoSubmitEndorsements))

	// EvidenceService is optional: without it the registry is still a
	// complete proposal path, just one whose pull requests arrive without
	// the field data that motivated them.
	if cfg.EvidenceEnabled {
		store, err := openEvidence(ctx, cfg)
		if err != nil {
			return err
		}
		defer store.Close()

		skillsv1.RegisterEvidenceServiceServer(grpcServer,
			evidenceserver.New(store, svc, cfg.EvidenceVerifyCommits))

		go retentionLoop(ctx, store, cfg.EvidenceRollupInterval, cfg.EvidenceRetention)
		go backupLoop(ctx, store, cfg.EvidenceBackupPath, cfg.EvidenceBackupInterval)
	} else {
		slog.Info("EvidenceService is disabled; no outcome reports will be collected")
	}

	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	reflection.Register(grpcServer)

	go func() {
		<-ctx.Done()
		slog.Info("shutdown signal received, draining connections")
		grpcServer.GracefulStop()
	}()

	slog.Info("skillsd-registry listening", "addr", cfg.GRPCAddr)
	return grpcServer.Serve(lis)
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
