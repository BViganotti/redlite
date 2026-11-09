// Package redlite runs a Redis-compatible server inside your process.
//
// It exists for tests: start a server on an ephemeral port, point an ordinary
// Redis client at Addr, and stop it when the test ends. No binary, container or
// background service required.
//
//	srv, err := redlite.Start(redlite.Config{Port: 0, Dir: t.TempDir()})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer srv.Close(context.Background())
//
//	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
package redlite

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/BViganotti/redlite/internal/config"
	"github.com/BViganotti/redlite/internal/server"
)

// Config describes an embedded server. The zero value is safe: it listens on
// loopback with an ephemeral port, keeps data in the working directory, and
// logs nothing.
type Config struct {
	// Bind is the address to listen on. Empty means 127.0.0.1.
	Bind string
	// Port is the TCP port. Zero picks a free port, which Addr reports.
	Port int
	// Dir holds the append-only file when AppendOnly is set. Empty means ".".
	Dir string
	// AppendOnly enables persistence.
	AppendOnly bool
	// AppendFsync is "always", "everysec" or "no". Empty means "everysec".
	AppendFsync string
	// MaxClients defaults to 10000.
	MaxClients int
	// LogLevel is "debug", "info", "warn" or "error". Empty means "error".
	LogLevel string
	// Timeout is the idle client timeout. Zero disables it.
	Timeout time.Duration
	// Logger receives server logs. Nil discards them.
	Logger *slog.Logger
	// RequirePass enables authentication. Empty means no password.
	RequirePass string
	// MasterAuth is the password sent to a primary when replicating.
	MasterAuth string
	// TLSPort enables a TLS listener when non-zero.
	TLSPort int
	// TLSCertFile and TLSKeyFile are the PEM certificate and key.
	TLSCertFile string
	TLSKeyFile  string
	// TLSCACertFile, when set, requires and verifies client certificates.
	TLSCACertFile string
	// TLSReplication makes the replica dial its primary over TLS.
	TLSReplication bool
	// ReplBacklogSize bounds the replication backlog in bytes.
	ReplBacklogSize int64
}

// Server is a running embedded instance.
type Server struct {
	srv *server.Server
}

// Start binds and serves until Close is called.
func Start(cfg Config) (*Server, error) {
	settings := config.Default()
	settings.Bind = "127.0.0.1"
	if cfg.Bind != "" {
		settings.Bind = cfg.Bind
	}
	settings.Port = cfg.Port
	settings.Dir = "."
	if cfg.Dir != "" {
		settings.Dir = cfg.Dir
	}
	settings.AppendOnly = cfg.AppendOnly
	settings.AppendFsync = "everysec"
	if cfg.AppendFsync != "" {
		settings.AppendFsync = cfg.AppendFsync
	}
	settings.MaxClients = 10000
	if cfg.MaxClients > 0 {
		settings.MaxClients = cfg.MaxClients
	}
	settings.LogLevel = "error"
	if cfg.LogLevel != "" {
		settings.LogLevel = cfg.LogLevel
	}
	settings.Timeout = cfg.Timeout
	settings.RequirePass = cfg.RequirePass
	settings.MasterAuth = cfg.MasterAuth
	settings.TLSPort = cfg.TLSPort
	settings.TLSCertFile = cfg.TLSCertFile
	settings.TLSKeyFile = cfg.TLSKeyFile
	settings.TLSCACertFile = cfg.TLSCACertFile
	settings.TLSReplication = cfg.TLSReplication
	if cfg.ReplBacklogSize > 0 {
		settings.ReplBacklogSize = cfg.ReplBacklogSize
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	level := new(slog.LevelVar)
	srv, err := server.New(settings, logger, level)
	if err != nil {
		return nil, err
	}
	if err := srv.Listen(); err != nil {
		_ = srv.Shutdown(context.Background())
		return nil, err
	}
	go func() {
		if err := srv.Serve(); err != nil {
			logger.Error("redlite serve stopped", "err", err)
		}
	}()

	return &Server{srv: srv}, nil
}

// Addr returns the bound address, for example "127.0.0.1:54321".
func (s *Server) Addr() string {
	addr := s.srv.Addr()
	if addr == nil {
		return ""
	}
	return addr.String()
}

// Close stops accepting connections, drains clients, and flushes the
// append-only file. It is safe to call more than once.
func (s *Server) Close(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
