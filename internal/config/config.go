package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// defaultMaxRequestBodyBytes and defaultMaxResultBytes apply when their
// respective env vars are unset. The SDK's own request-body default is
// 4 MiB; this is raised a little so a handful of skill/context files in
// one response don't run into it. defaultMaxResultBytes is deliberately
// small relative to the old gRPC send limit: a gRPC response was consumed
// by a client process that could buffer or discard it, but a tool result
// is injected into a model's context window, where the budget that matters
// is tokens, not bytes on a socket.
const (
	defaultMaxRequestBodyBytes = 8 << 20   // 8 MiB
	defaultMaxResultBytes      = 256 << 10 // 256 KiB
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	// HTTPAddr is the address the MCP server listens on over Streamable
	// HTTP, e.g. ":8080".
	HTTPAddr string

	// SkillsDir is the local directory that skill definitions are read
	// from. It is expected to be a volume populated by a git-clone init
	// container before this process starts.
	SkillsDir string

	// SkillsSubPath is an optional subdirectory within SkillsDir (or,
	// equivalently, within the cloned repo) under which skill directories
	// actually live. Leave empty if skills sit at the repo root.
	SkillsSubPath string

	// SkillsCommit overrides the commit SHA stamped onto every served
	// skill. Normally left unset: skillsd reads HEAD out of the cloned
	// SkillsDir itself. Set it when the skills directory isn't a git
	// working copy (a baked image layer, a ConfigMap) but the revision is
	// still known to whoever built it - without a commit, outcome reports
	// about these skills can't be attributed to a version.
	SkillsCommit string

	// MaxRequestBodyBytes caps a single incoming MCP request body.
	MaxRequestBodyBytes int64

	// MaxResultBytes caps the context-file content one get_skill call
	// returns. Unlike MaxRequestBodyBytes, this isn't transport-enforced -
	// it's a budget internal/toolresult applies when building a reply, and
	// exceeding it drops whole files rather than truncating one, always
	// naming what was left out.
	MaxResultBytes int

	// LogLevel is the minimum slog level emitted by the process, e.g.
	// "debug", "info", "warn", "error".
	LogLevel slog.Level
}

// Load reads configuration from environment variables, applying defaults
// where possible.
func Load() (Config, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("parsing LOG_LEVEL: %w", err)
	}

	maxRequestBodyBytes, err := getenvInt("MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodyBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_REQUEST_BODY_BYTES: %w", err)
	}
	maxResultBytes, err := getenvInt("MAX_RESULT_BYTES", defaultMaxResultBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing MAX_RESULT_BYTES: %w", err)
	}

	cfg := Config{
		HTTPAddr:            getenv("HTTP_ADDR", ":8080"),
		SkillsDir:           getenv("SKILLS_DIR", "/skills"),
		SkillsSubPath:       getenv("SKILLS_SUBPATH", ""),
		SkillsCommit:        getenv("SKILLS_COMMIT", ""),
		MaxRequestBodyBytes: int64(maxRequestBodyBytes),
		MaxResultBytes:      maxResultBytes,
		LogLevel:            level,
	}
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
