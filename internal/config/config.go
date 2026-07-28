package config

import (
	"fmt"
	"log/slog"
	"os"
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	// GRPCAddr is the address the gRPC server listens on, e.g. ":8080".
	GRPCAddr string

	// SkillsDir is the local directory that skill definitions are read
	// from. It is expected to be a volume populated by a git-clone init
	// container before this process starts.
	SkillsDir string

	// SkillsSubPath is an optional subdirectory within SkillsDir (or,
	// equivalently, within the cloned repo) under which skill directories
	// actually live. Leave empty if skills sit at the repo root.
	SkillsSubPath string

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

	cfg := Config{
		GRPCAddr:      getenv("GRPC_ADDR", ":8080"),
		SkillsDir:     getenv("SKILLS_DIR", "/skills"),
		SkillsSubPath: getenv("SKILLS_SUBPATH", ""),
		LogLevel:      level,
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
