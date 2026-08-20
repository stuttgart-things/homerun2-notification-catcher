package config

import (
	"log/slog"
	"os"
	"strings"

	homerun "github.com/stuttgart-things/homerun-library/v3"
)

func LoadRedisConfig() homerun.RedisConfig {
	return homerun.RedisConfig{
		Addr:     homerun.GetEnv("REDIS_ADDR", "localhost"),
		Port:     homerun.GetEnv("REDIS_PORT", "6379"),
		Password: homerun.GetEnv("REDIS_PASSWORD", ""),
		Stream:   homerun.GetEnv("REDIS_STREAM", "alerts"),
	}
}

// LoadStreams returns the list of Redis streams the catcher should subscribe to.
//
// Resolution:
//  1. REDIS_STREAMS (comma-separated) — preferred, enables multi-stream subscription
//  2. REDIS_STREAM — legacy single-stream env var, wrapped in a one-element list
//  3. ["alerts"] — hardcoded default
func LoadStreams() []string {
	return ParseStreams(os.Getenv("REDIS_STREAMS"), os.Getenv("REDIS_STREAM"))
}

// ParseStreams is the pure resolution helper behind LoadStreams. Exposed for tests.
func ParseStreams(streamsEnv, streamFallback string) []string {
	if streamsEnv != "" {
		parts := strings.Split(streamsEnv, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if streamFallback != "" {
		return []string{streamFallback}
	}
	return []string{"alerts"}
}

// SetupLogging configures slog as the default logger based on LOG_FORMAT and LOG_LEVEL env vars.
func SetupLogging() {
	format := strings.ToLower(homerun.GetEnv("LOG_FORMAT", "json"))
	levelStr := strings.ToLower(homerun.GetEnv("LOG_LEVEL", "info"))

	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))

	// homerun-library is silent by default as of v3.2.0. Routing it through the
	// same logger means its records arrive in this service's format and at this
	// service's level, instead of the pterm-decorated stdout writes it used to
	// interleave into the log stream.
	homerun.SetLogger(slog.Default())
}
