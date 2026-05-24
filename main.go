package main

import (
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/catcher"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"

	homerun "github.com/stuttgart-things/homerun-library/v3"
)

// Build-time variables set via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	config.SetupLogging()

	slog.Info("starting homerun2-notification-catcher",
		"version", version,
		"commit", commit,
		"date", date,
		"go", runtime.Version(),
	)

	redisConfig := config.LoadRedisConfig()
	streams := config.LoadStreams()
	consumerGroup := homerun.GetEnv("CONSUMER_GROUP", "homerun2-notification-catcher")
	consumerName := homerun.GetEnv("CONSUMER_NAME", "")

	// Phase 1 scaffold: log handler only. The notify dispatch layer
	// (internal/notify) is added in #6 / #7.
	handlers := []catcher.MessageHandler{catcher.LogHandler()}

	c, err := catcher.NewRedisCatcher(redisConfig, streams, consumerGroup, consumerName, handlers...)
	if err != nil {
		slog.Error("failed to create catcher", "error", err)
		os.Exit(1)
	}

	slog.Info("catcher configured",
		"redis_addr", redisConfig.Addr,
		"redis_port", redisConfig.Port,
		"streams", streams,
		"consumer_group", consumerGroup,
	)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	if errCh := c.Errors(); errCh != nil {
		go func() {
			for err := range errCh {
				slog.Error("consumer error", "error", err)
			}
		}()
	}

	go func() {
		<-quit
		slog.Info("shutting down catcher")
		c.Shutdown()
	}()

	slog.Info("catcher running, waiting for messages...")
	c.Run()

	slog.Info("catcher exited gracefully")
}
