package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/araghukas/skillset/internal/clientguide"
	"github.com/araghukas/skillset/internal/config"
	"github.com/araghukas/skillset/internal/gitrev"
	"github.com/araghukas/skillset/internal/mcphttp"
	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/skilltools"
	"github.com/araghukas/skillset/internal/storage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	catalog := reg.Catalog()

	srv := mcp.NewServer(
		&mcp.Implementation{Name: "skillsd", Version: version},
		&mcp.ServerOptions{
			Instructions: clientguide.Instructions("skillsd", catalog),
			// Suppress the SDK's default advertisement of a "logging"
			// capability, which this server does not implement.
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	skilltools.Add(srv, reg, cfg.MaxResultBytes, catalog)

	return mcphttp.Serve(ctx, srv, mcphttp.Options{
		Addr:                cfg.HTTPAddr,
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
