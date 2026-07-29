package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
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

	svc := proposals.New(repo, cfg.SkillsSubPath, cfg.MaxFileContentBytes)
	gh := githubpr.New(cfg.GitHubAPIBaseURL, cfg.GitHubOwner, cfg.GitHubRepo, cfg.GitHubToken)

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
	skillsv1.RegisterProposalServiceServer(grpcServer, proposalserver.New(svc, gh, cfg.SkillsRepoBaseBranch))
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
