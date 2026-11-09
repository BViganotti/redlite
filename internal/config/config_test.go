package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Bind != "127.0.0.1" {
		t.Errorf("Bind = %q, want loopback", cfg.Bind)
	}
	if cfg.Port != 6379 {
		t.Errorf("Port = %d, want 6379", cfg.Port)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestAddrAndAOFPath(t *testing.T) {
	cfg := Default()
	cfg.Bind = "0.0.0.0"
	cfg.Port = 7000
	cfg.Dir = "/var/lib/redlite"

	if got, want := cfg.Addr(), "0.0.0.0:7000"; got != want {
		t.Errorf("Addr() = %q, want %q", got, want)
	}
	if got, want := cfg.AOFPath(), "/var/lib/redlite/appendonly.aof"; got != want {
		t.Errorf("AOFPath() = %q, want %q", got, want)
	}
}

func TestLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"bogus":   slog.LevelInfo,
	}
	for name, want := range tests {
		cfg := Default()
		cfg.LogLevel = name
		if got := cfg.Level(); got != want {
			t.Errorf("Level(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidateRejectsBadSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"port", func(c *Config) { c.Port = 70000 }, "port"},
		{"maxclients", func(c *Config) { c.MaxClients = 0 }, "maxclients"},
		{"appendfsync", func(c *Config) { c.AppendFsync = "sometimes" }, "appendfsync"},
		{"loglevel", func(c *Config) { c.LogLevel = "verbose" }, "loglevel"},
		{"dir", func(c *Config) { c.Dir = "" }, "dir"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

func TestValidateAllowsEphemeralPort(t *testing.T) {
	cfg := Default()
	cfg.Port = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("port 0 should be allowed: %v", err)
	}
}

func TestValidateAcceptsZeroTimeout(t *testing.T) {
	cfg := Default()
	cfg.Timeout = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("zero timeout should be allowed: %v", err)
	}
	cfg.Timeout = 30 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("positive timeout should be allowed: %v", err)
	}
}
