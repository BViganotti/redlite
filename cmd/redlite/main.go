// Command redlite runs a Redis-compatible server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BViganotti/redlite/internal/config"
	"github.com/BViganotti/redlite/internal/server"
	"github.com/BViganotti/redlite/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redlite:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := config.Default()

	flags := flag.NewFlagSet("redlite", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&cfg.Bind, "bind", cfg.Bind, "address to bind (default loopback)")
	flags.IntVar(&cfg.Port, "port", cfg.Port, "TCP port to listen on")
	flags.StringVar(&cfg.Dir, "dir", cfg.Dir, "data directory for the append-only file")
	flags.BoolVar(&cfg.AppendOnly, "appendonly", cfg.AppendOnly, "enable append-only file persistence")
	flags.StringVar(&cfg.AppendFsync, "appendfsync", cfg.AppendFsync, "fsync policy: always, everysec or no")
	flags.StringVar(&cfg.LogLevel, "loglevel", cfg.LogLevel, "log level: debug, info, warn or error")
	flags.IntVar(&cfg.MaxClients, "maxclients", cfg.MaxClients, "maximum number of concurrent clients")
	flags.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "idle client timeout, 0 disables")
	showVersion := flags.Bool("version", false, "print the version and exit")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("redlite", version.String())
		return nil
	}

	level := new(slog.LevelVar)
	level.Set(cfg.Level())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	srv, err := server.New(cfg, logger, level)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil {
			logger.Error("server stopped", "err", err)
		}
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
