// Package config defines the server's runtime settings.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the complete server settings after flag parsing.
type Config struct {
	Bind                 string
	Port                 int
	Dir                  string
	DBFilename           string
	AppendOnly           bool
	AppendFsync          string
	LogLevel             string
	MaxClients           int
	Timeout              time.Duration
	RequirePass          string
	MasterAuth           string
	NotifyKeyspaceEvents string
	TLSPort              int
	TLSCertFile          string
	TLSKeyFile           string
	TLSCACertFile        string
	TLSReplication       bool
	ReplBacklogSize      int64
}

// Default returns the settings used when no flags are supplied.
//
// The default bind address is loopback: an unauthenticated Redis-compatible
// server has no business listening on every interface by accident.
func Default() Config {
	return Config{
		Bind:                 "127.0.0.1",
		Port:                 6379,
		Dir:                  ".",
		DBFilename:           "dump.rdb",
		AppendOnly:           false,
		AppendFsync:          "everysec",
		LogLevel:             "info",
		MaxClients:           10000,
		Timeout:              0,
		RequirePass:          "",
		MasterAuth:           "",
		NotifyKeyspaceEvents: "",
		ReplBacklogSize:      1 << 20,
	}
}

// Addr returns the TCP address to listen on.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.Port)
}

// TLSAddr returns the TLS address to listen on.
func (c Config) TLSAddr() string {
	return fmt.Sprintf("%s:%d", c.Bind, c.TLSPort)
}

// TLSPortString returns the TLS port for INFO.
func (c Config) TLSPortString() string {
	return strconv.Itoa(c.TLSPort)
}

// AOFPath returns the append-only file location inside Dir.
func (c Config) AOFPath() string {
	return filepath.Join(c.Dir, "appendonly.aof")
}

// SnapshotPath returns the snapshot location inside Dir.
func (c Config) SnapshotPath() string {
	return filepath.Join(c.Dir, c.DBFilename)
}

// Level maps LogLevel to a slog level.
func (c Config) Level() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Validate rejects settings the server cannot honour.
func (c Config) Validate() error {
	var problems []error
	if c.Port < 0 || c.Port > 65535 {
		problems = append(problems, fmt.Errorf("port %d is out of range", c.Port))
	}
	if c.MaxClients < 1 {
		problems = append(problems, errors.New("maxclients must be at least 1"))
	}
	switch c.AppendFsync {
	case "always", "everysec", "no":
	default:
		problems = append(problems, fmt.Errorf("appendfsync %q must be always, everysec or no", c.AppendFsync))
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		problems = append(problems, fmt.Errorf("loglevel %q must be debug, info, warn or error", c.LogLevel))
	}
	if c.Dir == "" {
		problems = append(problems, errors.New("dir must not be empty"))
	}
	if c.DBFilename == "" {
		problems = append(problems, errors.New("dbfilename must not be empty"))
	}
	if strings.ContainsAny(c.DBFilename, `/\`) {
		problems = append(problems, errors.New("dbfilename must not contain a path separator"))
	}
	if c.TLSPort < 0 || c.TLSPort > 65535 {
		problems = append(problems, fmt.Errorf("tls-port %d is out of range", c.TLSPort))
	}
	if c.TLSPort > 0 {
		if c.TLSCertFile == "" || c.TLSKeyFile == "" {
			problems = append(problems, errors.New("tls-cert-file and tls-key-file are required when tls-port is set"))
		}
		if c.TLSPort == c.Port {
			problems = append(problems, errors.New("tls-port must differ from port"))
		}
	}
	if c.ReplBacklogSize <= 0 {
		problems = append(problems, errors.New("repl-backlog-size must be positive"))
	}
	return errors.Join(problems...)
}
