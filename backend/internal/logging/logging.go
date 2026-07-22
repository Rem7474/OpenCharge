// Package logging builds the process-wide structured logger shared by both
// binaries (opencharge-api, opencharge-ingest). JSON to stdout, not plain
// text: the whole point of moving off the stdlib log package is that log
// lines become machine-parseable (source, counts, durations as real fields
// instead of buried in a free-text sentence), which plain text can't give
// you regardless of which package writes it.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New builds the shared logger. Callers are expected to install it with
// slog.SetDefault so every package can just call the slog.Info/Warn/Error
// package funcs without threading a *slog.Logger through every
// ingester/handler constructor.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: levelFromEnv()}))
}

// levelFromEnv reads LOG_LEVEL (debug/info/warn/error, case-insensitive);
// unset or unrecognized defaults to info, same fallback convention as this
// package's getEnv-style helpers elsewhere in the two main.go files.
func levelFromEnv() slog.Level {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
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
