// Package mcphttp serves an MCP server over Streamable HTTP, alongside a
// health endpoint for Kubernetes probes. It is shared by skillsd and
// skillsd-registry, which differ only in which tools they register.
package mcphttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HealthPath is the endpoint Kubernetes readiness and liveness probes hit.
// It is deliberately outside the MCP path: a probe should not have to
// speak JSON-RPC to learn whether the process is up.
const HealthPath = "/healthz"

// DefaultPath is where the MCP endpoint is mounted when Options.Path is
// empty.
const DefaultPath = "/mcp"

// shutdownGrace bounds how long in-flight requests have to finish after a
// SIGTERM before the listener is closed regardless.
const shutdownGrace = 10 * time.Second

// Options configures Serve.
type Options struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string

	// Path is where the MCP endpoint is mounted. Defaults to DefaultPath.
	Path string

	// MaxRequestBodyBytes caps an incoming request body. Zero leaves the
	// SDK's own default in place.
	MaxRequestBodyBytes int64
}

// Serve runs srv over Streamable HTTP until ctx is cancelled, then drains
// in-flight requests and returns.
//
// The session is stateless. skillsd runs N replicas behind a Service with
// no session affinity, so a session pinned to one replica would break as
// soon as a client's second request landed on another; the registry uses
// the same mode so both behave identically across a restart. Neither
// server initiates requests to the client, which is the capability
// stateless mode gives up.
func Serve(ctx context.Context, srv *mcp.Server, opts Options) error {
	path := opts.Path
	if path == "" {
		path = DefaultPath
	}

	mux := http.NewServeMux()
	mux.Handle(path, mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:           true,
			MaxRequestBodyBytes: opts.MaxRequestBodyBytes,
			// Without this a client hanging up leaves the handler running
			// to completion against a connection nobody is reading.
			PropagateRequestCancellation: true,
		},
	))
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	httpSrv := &http.Server{
		Addr:    opts.Addr,
		Handler: mux,
		// No ReadTimeout or WriteTimeout: a tool call can legitimately take
		// a while (a git push, a pull request round-trip), and a write
		// deadline would sever it mid-flight. ReadHeaderTimeout still
		// bounds the cheap slowloris case.
		ReadHeaderTimeout: 10 * time.Second,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		slog.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}()

	slog.Info("listening", "addr", opts.Addr, "mcp_path", path, "health_path", HealthPath)
	err := httpSrv.ListenAndServe()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
