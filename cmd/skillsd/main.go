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
	"github.com/araghukas/skillset/internal/config"
	"github.com/araghukas/skillset/internal/registry"
	"github.com/araghukas/skillset/internal/server"
	"github.com/araghukas/skillset/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

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
	reg := registry.New(backend, cfg.SkillsSubPath)

	// The skills directory is populated by a git-clone init container
	// before this process starts, so there's exactly one load: if it
	// fails, there's no runtime path to recovery within this pod, so fail
	// startup and let Kubernetes restart it (rerunning the init container).
	count, err := reg.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading skill index: %w", err)
	}
	slog.Info("loaded skill index", "count", count)

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}

	grpcServer := grpc.NewServer()
	skillsv1.RegisterSkillServiceServer(grpcServer, server.New(reg))
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	reflection.Register(grpcServer)

	go func() {
		<-ctx.Done()
		slog.Info("shutdown signal received, draining connections")
		grpcServer.GracefulStop()
	}()

	slog.Info("skillsd listening", "addr", cfg.GRPCAddr)
	return grpcServer.Serve(lis)
}
