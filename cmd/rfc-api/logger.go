package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/donaldgifford/rfc-api/internal/config"
)

// buildLogger returns a slog.Logger configured from cfg. Defaults on
// either field produce safe behavior: info level + JSON handler.
//
// Called after config load; the bootstrap logger in main() is the
// fallback for errors that happen before this point.
func buildLogger(cfg config.Log, baseAttrs ...slog.Attr) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(cfg.Level),
	}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	if len(baseAttrs) > 0 {
		anyAttrs := make([]any, 0, len(baseAttrs))
		for _, a := range baseAttrs {
			anyAttrs = append(anyAttrs, a)
		}
		logger = logger.With(anyAttrs...)
	}
	return logger
}

// parseLevel maps cfg.Log.Level strings to slog levels. Unknown
// levels (typos, empty) fall back to info rather than erroring --
// boot should not fail on a malformed log level.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info", "":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}
