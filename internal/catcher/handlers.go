package catcher

import (
	"context"
	"log/slog"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// LogHandler returns a MessageHandler that logs messages with severity-aware levels.
func LogHandler() MessageHandler {
	return func(msg models.CaughtMessage) {
		level := severityToLevel(msg.Severity)

		slog.Log(context.Background(), level, "message caught",
			"objectId", msg.ObjectID,
			"streamId", msg.StreamID,
			"title", msg.Title,
			"message", msg.Message.Message,
			"severity", msg.Severity,
			"author", msg.Author,
			"system", msg.System,
			"timestamp", msg.Timestamp,
			"tags", msg.Tags,
		)
	}
}

func severityToLevel(severity string) slog.Level {
	switch severity {
	case "error", "critical":
		return slog.LevelError
	case "warning":
		return slog.LevelWarn
	case "debug":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}
