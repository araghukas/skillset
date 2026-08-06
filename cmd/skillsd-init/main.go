// Command skillsd-init clones the skills repository into the volume skillsd
// serves from. It runs as skillsd's init container, once, before the serving
// process starts.
//
// It exists as a Go binary rather than a `git clone` shell one-liner because
// GitHub App auth needs a signed JWT exchanged for a short-lived token
// before the clone can happen - RS256 in shell, via openssl and hand-rolled
// base64url, is not something worth having on the startup path. Sharing
// internal/githubauth with skillsd-registry also means both components read
// the same environment and behave the same way.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/araghukas/skillset/internal/githubauth"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func main() {
	if err := run(); err != nil {
		slog.Error("skillsd-init exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var level slog.Level
	if err := level.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return fmt.Errorf("parsing LOG_LEVEL: %w", err)
	}
	slog.SetLogLoggerLevel(level)

	url := os.Getenv("SKILLS_REPO_URL")
	if url == "" {
		return fmt.Errorf("SKILLS_REPO_URL is required")
	}
	branch := getenv("SKILLS_REPO_BRANCH", "main")
	dir := getenv("SKILLS_DIR", "/skills")

	tokens, mode, err := githubauth.LoadFromEnv()
	if err != nil {
		return err
	}

	var auth *githttp.BasicAuth
	if tokens != nil {
		token, err := tokens.Token(ctx)
		if err != nil {
			return err
		}
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}

	slog.Info("cloning skills repo", "url", url, "branch", branch, "dir", dir, "github_auth_mode", mode)

	// Shallow and single-branch: skillsd serves a snapshot and never
	// refreshes it (see docs/skillsd.md), so history it can't use is pure
	// startup latency on every pod.
	_, err = git.PlainCloneContext(ctx, dir, false, &git.CloneOptions{
		URL:           url,
		Auth:          auth,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return fmt.Errorf("cloning %s into %s: %w", url, dir, err)
	}

	slog.Info("clone complete", "dir", dir)
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
