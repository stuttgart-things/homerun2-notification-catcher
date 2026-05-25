package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/catcher"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"

	homerun "github.com/stuttgart-things/homerun-library/v3"
)

// Build-time variables set via ldflags
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

// dispatchTimeout caps the whole fan-out per caught message so one slow
// Notifier can't hold a consumer worker indefinitely. Individual HTTP calls
// have their own (shorter) timeouts.
const dispatchTimeout = 30 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "smoke" {
		if err := runSmoke(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "smoke:", err)
			os.Exit(1)
		}
		return
	}

	if err := runServer(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func runServer() error {
	config.SetupLogging()

	slog.Info("starting homerun2-notification-catcher",
		"version", version,
		"commit", commit,
		"date", date,
		"go", runtime.Version(),
	)

	notifyCfg, err := config.LoadNotifyConfigPath()
	if err != nil {
		return err
	}
	dispatcher, err := notify.NewDispatcher(notifyCfg, nil)
	if err != nil {
		return err
	}
	dispatcher.DryRun = boolEnv("DRY_RUN")
	slog.Info("notify dispatcher loaded",
		"outputs", dispatcher.OutputNames(),
		"dry_run", dispatcher.DryRun,
	)

	dispatchHandler := func(cm models.CaughtMessage) {
		ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
		defer cancel()
		dispatcher.Dispatch(ctx, cm.Message)
	}

	redisConfig := config.LoadRedisConfig()
	streams := config.LoadStreams()
	consumerGroup := homerun.GetEnv("CONSUMER_GROUP", "homerun2-notification-catcher")
	consumerName := homerun.GetEnv("CONSUMER_NAME", "")

	c, err := catcher.NewRedisCatcher(redisConfig, streams, consumerGroup, consumerName,
		catcher.LogHandler(),
		dispatchHandler,
	)
	if err != nil {
		return fmt.Errorf("create catcher: %w", err)
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
	return nil
}

// runSmoke fires one synthetic homerun.Message through the YAML-configured
// outputs without touching Redis. Used to verify a Teams webhook or routing
// rule before promoting a config to deploy.
//
//	notification-catcher smoke \
//	    --config notify.yaml \
//	    --title "disk almost full" \
//	    --severity warning \
//	    --system kubernetes \
//	    --tags infra,storage
//
// Kept in main.go (not smoke.go) because the external Dagger Go module builds
// `main.go` as a single file rather than the whole package — splitting the
// subcommand into its own file would break CI.
func runSmoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		cfgPath         = fs.String("config", "", "path to notify config YAML (required)")
		title           = fs.String("title", "smoke test", "Message.Title")
		body            = fs.String("message", "smoke-test message from notification-catcher", "Message.Message")
		severity        = fs.String("severity", "info", "Message.Severity (one of "+strings.Join(config.KnownSeverities, "|")+")")
		author          = fs.String("author", "notification-catcher smoke", "Message.Author")
		system          = fs.String("system", "", "Message.System")
		tags            = fs.String("tags", "", "Message.Tags (comma-separated)")
		url             = fs.String("url", "", "Message.Url")
		assigneeName    = fs.String("assignee-name", "", "Message.AssigneeName")
		assigneeAddress = fs.String("assignee-address", "", "Message.AssigneeAddress")
		timeoutFlag     = fs.Duration("timeout", 30*time.Second, "total timeout for the fan-out")
		dryRun          = fs.Bool("dry-run", boolEnv("DRY_RUN"), "log which outputs would fire but do not invoke any Notifier")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*cfgPath) == "" {
		fs.Usage()
		return fmt.Errorf("--config is required")
	}

	cfg, err := config.LoadNotifyConfig(*cfgPath)
	if err != nil {
		return err
	}

	dispatcher, err := notify.NewDispatcher(cfg, nil)
	if err != nil {
		return err
	}
	dispatcher.DryRun = *dryRun

	msg := homerun.Message{
		Title:           *title,
		Message:         *body,
		Severity:        *severity,
		Author:          *author,
		System:          *system,
		Tags:            *tags,
		Url:             *url,
		AssigneeName:    *assigneeName,
		AssigneeAddress: *assigneeAddress,
		Timestamp:       time.Now().Format(time.RFC3339),
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	results := dispatcher.Dispatch(ctx, msg)
	return printSmokeResults(results)
}

func printSmokeResults(results []notify.DispatchResult) error {
	if len(results) == 0 {
		fmt.Println("no outputs configured")
		return nil
	}
	var failed int
	for _, r := range results {
		switch {
		case r.Skipped:
			fmt.Printf("- %s: SKIPPED (filters did not match)\n", r.Output)
		case r.DryRun:
			fmt.Printf("- %s: DRY-RUN (filters matched; Send not invoked)\n", r.Output)
		case r.Err != nil:
			failed++
			fmt.Printf("- %s: FAIL: %v\n", r.Output, r.Err)
		default:
			fmt.Printf("- %s: OK\n", r.Output)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d output(s) failed", failed)
	}
	return nil
}

// boolEnv parses a permissive truthy env var. Accepts "true", "1", "yes", "on"
// (case-insensitive). Anything else, including empty, is false. Used for
// DRY_RUN — the cost of misreading the value is too high to require a strict
// "true"/"false" only.
func boolEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}
