package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	skillsv1 "github.com/araghukas/skillset/gen/skills/v1"
	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/config"
	"github.com/araghukas/skillset/internal/gitrev"
	"github.com/araghukas/skillset/internal/mcphttp"
	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/server"
	"github.com/araghukas/skillset/internal/skilltools"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// version is stamped at build time; unset in `go run`/`go test` builds.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("skillsd exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetLogLoggerLevel(cfg.LogLevel)

	backend := storage.NewFSBackend(cfg.SkillsDir)
	reg := registry.New(backend, cfg.SkillsSubPath, resolveCommit(cfg))

	// The skills directory is populated by a git-clone init container
	// before this process starts, so there's exactly one load: if it
	// fails, there's no runtime path to recovery within this pod, so fail
	// startup and let Kubernetes restart it (rerunning the init container).
	count, err := reg.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading skill index: %w", err)
	}
	slog.Info("loaded skill index", "count", count)

	group, gctx := errgroup.WithContext(ctx)

	if cfg.GRPCAddr != "" {
		group.Go(func() error { return serveGRPC(gctx, cfg, reg) })
	}
	if cfg.MCPAddr != "" {
		group.Go(func() error { return serveMCP(gctx, cfg, reg) })
	}
	if cfg.GRPCAddr == "" && cfg.MCPAddr == "" {
		return fmt.Errorf("neither GRPC_ADDR nor MCP_ADDR is set; at least one transport must be enabled")
	}

	return group.Wait()
}

func serveGRPC(ctx context.Context, cfg config.Config, reg *registry.Registry) error {
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
	skillsv1.RegisterSkillServiceServer(grpcServer, server.New(reg))
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	reflection.Register(grpcServer)

	go func() {
		<-ctx.Done()
		slog.Info("shutdown signal received, draining gRPC connections")
		grpcServer.GracefulStop()
	}()

	slog.Info("skillsd listening (gRPC)", "addr", cfg.GRPCAddr)
	return grpcServer.Serve(lis)
}

// serveMCP runs the MCP server alongside gRPC. It shares the same
// registry, so both transports serve an identical view of the skill index.
func serveMCP(ctx context.Context, cfg config.Config, reg *registry.Registry) error {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "skillsd", Version: version},
		&mcp.ServerOptions{
			Instructions: clientguide.Instructions("skillsd"),
			// Suppress the SDK's default advertisement of a "logging"
			// capability, which this server does not implement.
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	skilltools.Add(srv, reg)

	return mcphttp.Serve(ctx, srv, mcphttp.Options{
		Addr:                cfg.MCPAddr,
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	})
}

// resolveCommit determines the revision to stamp onto every served skill:
// an explicit SKILLS_COMMIT if set, otherwise HEAD of the working copy the
// init container cloned into SkillsDir.
//
// A missing commit is degraded, not fatal. Pointing skillsd at a plain
// directory of skills is a supported way to run it, and refusing to start
// would turn provenance from a feature into a deployment requirement. The
// cost is that outcome reports about these skills can't be attributed to a
// version, which is worth a warning but not an outage.
func resolveCommit(cfg config.Config) string {
	if cfg.SkillsCommit != "" {
		return cfg.SkillsCommit
	}

	commit, err := gitrev.Head(cfg.SkillsDir)
	if err != nil {
		slog.Warn("could not determine skills revision; serving skills without a commit stamp",
			"dir", cfg.SkillsDir, "error", err,
			"hint", "set SKILLS_COMMIT if the skills directory is not a git working copy")
		return ""
	}
	return commit
}
