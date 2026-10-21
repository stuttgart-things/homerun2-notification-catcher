package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/server"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/sink"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/webui"

	homerun "github.com/stuttgart-things/homerun-library/v4"
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
	if len(os.Args) > 1 && os.Args[1] == "sink" {
		if err := runSink(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sink:", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "assert-card" {
		if err := runAssertCard(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "assert-card:", err)
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
		dispatcher.Dispatch(ctx, cm)
	}

	redisConfig := config.LoadRedisConfig()
	streams := config.LoadStreams()
	consumerGroup := homerun.GetEnv("CONSUMER_GROUP", "homerun2-notification-catcher")
	consumerName := homerun.GetEnv("CONSUMER_NAME", "")
	consumerStartID := homerun.GetEnv("CONSUMER_START_ID", catcher.DefaultStartID)

	// Canceled on SIGINT/SIGTERM: stops the Redis wait below, then the catcher.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Optional read-only HTTP surface (#22): health for the readiness probe,
	// the loaded config. Up before the Redis wait, so /healthz can say
	// "connecting" instead of refusing the connection.
	var ui *server.Server
	if addr := os.Getenv("HTTP_ADDR"); addr != "" {
		basePath, err := webui.ParseBasePath(os.Getenv("BASE_PATH"))
		if err != nil {
			return err
		}
		ui = server.New(server.Options{
			Token:         os.Getenv("AUTH_TOKEN"),
			BasePath:      basePath,
			Build:         server.BuildInfo{Version: version, Commit: commit, Date: date},
			Config:        notifyCfg,
			DryRun:        dispatcher.DryRun,
			RedisAddr:     fmt.Sprintf("%s:%s", redisConfig.Addr, redisConfig.Port),
			Streams:       streams,
			ConsumerGroup: consumerGroup,
		})
		srv := &http.Server{Addr: addr, Handler: ui.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("http server failed", "addr", addr, "error", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		slog.Info("http listening", "addr", addr, "base_path", basePath, "auth", os.Getenv("AUTH_TOKEN") != "")
	}

	// The consumer's preflight dials Redis exactly once, so a Redis that is
	// still starting used to end the process and crashloop the pod (#40).
	// Wait for it with bounded backoff for REDIS_STARTUP_TIMEOUT (default 120s).
	startupTimeout, err := homerun.LoadRedisStartupTimeout()
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := homerun.WaitForRedisContext(ctx, redisConfig, startupTimeout); err != nil {
		if ctx.Err() != nil {
			slog.Info("shutdown requested while waiting for redis")
			return nil
		}
		return fmt.Errorf("redis not reachable (startup_timeout %s): %w", startupTimeout, err)
	}

	c, err := catcher.NewRedisCatcher(redisConfig, streams, consumerGroup, consumerName, consumerStartID,
		catcher.LogHandler(),
		dispatchHandler,
	)
	if err != nil {
		return fmt.Errorf("create catcher: %w", err)
	}

	if ui != nil {
		ui.SetPing(c.Ping)
	}

	slog.Info("catcher configured",
		"redis_addr", redisConfig.Addr,
		"redis_port", redisConfig.Port,
		"streams", streams,
		"consumer_group", consumerGroup,
		"consumer_start_id", consumerStartID,
	)

	if errCh := c.Errors(); errCh != nil {
		go func() {
			for err := range errCh {
				slog.Error("consumer error", "error", err)
			}
		}()
	}

	go func() {
		<-ctx.Done()
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
		url             = fs.String("url", "", "Message.URL")
		assigneeName    = fs.String("assignee-name", "", "Message.AssigneeName")
		assigneeAddress = fs.String("assignee-address", "", "Message.AssigneeAddress")
		objectID        = fs.String("object-id", "", "objectId the outputs carry (card footer, webhook field); empty: none")
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
		URL:             *url,
		AssigneeName:    *assigneeName,
		AssigneeAddress: *assigneeAddress,
		Timestamp:       time.Now().Format(time.RFC3339),
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	results := dispatcher.Dispatch(ctx, models.CaughtMessage{Message: msg, ObjectID: *objectID})
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

// runSink serves the notification sink (#52): point an msteams output at
// http://<sink>/teams/<channel> or a webhook output at
// http://<sink>/webhook/<channel>, and every post shows up in the HTML view
// at / -- Teams posts rendered as Teams shows them -- and in
// GET /api/messages for tests. No Redis needed.
//
//	notification-catcher sink --addr :8080 --capacity 500
//
// SINK_TOKEN, when set, is required on every route but the health checks,
// as "Authorization: Bearer <token>" or "?token=<token>". BASE_PATH serves
// the UI under a prefix, as in homerun2-core-catcher.
func runSink(args []string) error {
	fs := flag.NewFlagSet("sink", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		addr     = fs.String("addr", homerun.GetEnv("SINK_ADDR", ":8080"), "listen address")
		capacity = fs.Int("capacity", sink.DefaultCapacity, "posts kept per channel (oldest drop first)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	config.SetupLogging()
	basePath, err := webui.ParseBasePath(os.Getenv("BASE_PATH"))
	if err != nil {
		return err
	}
	opts := sink.Options{
		Token:    os.Getenv("SINK_TOKEN"),
		BasePath: basePath,
		Build:    sink.BuildInfo{Version: version, Commit: commit, Date: date},
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           sink.NewServer(sink.NewStore(*capacity), opts).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("sink listening",
		"addr", *addr,
		"base_path", basePath,
		"capacity", *capacity,
		"auth", opts.Token != "",
		"version", version,
	)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runAssertCard polls a notification sink until posts match, for e2e tests
// (#62). Exit 0 prints the matching posts; exit 1 says what came closest.
//
//	notification-catcher assert-card \
//	    --sink http://homerun2-notification-catcher-sink.homerun2.svc \
//	    --channel platform --object-id "$OBJECT_ID" \
//	    --expect-fact System=homerun2-git-pitcher --timeout 60s
func runAssertCard(args []string) error {
	fs := flag.NewFlagSet("assert-card", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		a     sink.Assertion
		since string
	)
	sinkURL := fs.String("sink", os.Getenv("SINK_URL"), "sink base URL incl. BASE_PATH (env SINK_URL, required)")
	token := fs.String("token", os.Getenv("SINK_TOKEN"), "sink token (env SINK_TOKEN)")
	timeout := fs.Duration("timeout", 60*time.Second, "give up after this long")
	interval := fs.Duration("interval", time.Second, "time between polls")
	fs.StringVar(&a.Kind, "kind", sink.KindTeams, "post kind: msteams, webhook, or empty for both")
	fs.StringVar(&a.Channel, "channel", "", "channel (last path segment of the output URL)")
	fs.StringVar(&a.ObjectID, "object-id", "", "only posts of this message (its objectId)")
	fs.StringVar(&a.Contains, "contains", "", "only posts whose body contains this text")
	fs.StringVar(&since, "since", "", "only posts received after this: a duration ago (10m) or RFC3339")
	fs.Func("expect-fact", "Title=Value the post must carry (repeatable)", func(v string) error {
		title, value, ok := strings.Cut(v, "=")
		if !ok || strings.TrimSpace(title) == "" {
			return errors.New("want Title=Value")
		}
		if a.Facts == nil {
			a.Facts = map[string]string{}
		}
		a.Facts[strings.TrimSpace(title)] = strings.TrimSpace(value)
		return nil
	})
	fs.StringVar(&a.Title, "expect-title", "", "substring the title must contain")
	fs.StringVar(&a.Severity, "expect-severity", "", "severity the post must have")
	fs.IntVar(&a.Count, "count", 1, "at least this many posts must match")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sinkURL == "" {
		return errors.New("--sink (or SINK_URL) is required")
	}
	if since != "" {
		if d, err := time.ParseDuration(since); err == nil {
			a.Since = time.Now().Add(-d)
		} else if t, err := time.Parse(time.RFC3339, since); err == nil {
			a.Since = t
		} else {
			return fmt.Errorf("--since %q: want a duration or RFC3339 time", since)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &sink.AssertClient{
		BaseURL:  strings.TrimSuffix(*sinkURL, "/"),
		Token:    *token,
		Interval: *interval,
	}
	posts, err := client.AssertPosts(ctx, a)
	if err != nil {
		return err
	}
	for _, m := range posts {
		fmt.Println("OK", sink.Describe(m))
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
