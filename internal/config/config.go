package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// defaultGRPCMaxRecvMsgSizeBytes and defaultGRPCMaxSendMsgSizeBytes apply
// when their respective env vars are unset. gRPC-Go itself defaults both to
// 4 MiB; these are raised a little so a handful of skill/context files in
// one response don't run into the transport limit, while still bounding
// memory per request/response rather than leaving it unbounded.
const (
	defaultGRPCMaxRecvMsgSizeBytes = 8 << 20 // 8 MiB
	defaultGRPCMaxSendMsgSizeBytes = 8 << 20 // 8 MiB
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

	// GRPCMaxRecvMsgSizeBytes caps the size of a single incoming gRPC
	// message (grpc.MaxRecvMsgSize).
	GRPCMaxRecvMsgSizeBytes int

	// GRPCMaxSendMsgSizeBytes caps the size of a single outgoing gRPC
	// message (grpc.MaxSendMsgSize) - relevant here for GetSkillAtRef
	// responses that include context files.
	GRPCMaxSendMsgSizeBytes int

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

	maxRecvMsgSize, err := getenvInt("GRPC_MAX_RECV_MSG_SIZE_BYTES", defaultGRPCMaxRecvMsgSizeBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing GRPC_MAX_RECV_MSG_SIZE_BYTES: %w", err)
	}
	maxSendMsgSize, err := getenvInt("GRPC_MAX_SEND_MSG_SIZE_BYTES", defaultGRPCMaxSendMsgSizeBytes)
	if err != nil {
		return Config{}, fmt.Errorf("parsing GRPC_MAX_SEND_MSG_SIZE_BYTES: %w", err)
	}

	cfg := Config{
		GRPCAddr:                getenv("GRPC_ADDR", ":8080"),
		SkillsDir:               getenv("SKILLS_DIR", "/skills"),
		SkillsSubPath:           getenv("SKILLS_SUBPATH", ""),
		GRPCMaxRecvMsgSizeBytes: maxRecvMsgSize,
		GRPCMaxSendMsgSizeBytes: maxSendMsgSize,
		LogLevel:                level,
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
