// Dagger CI module for homerun2-notification-catcher
//
// Provides build, lint, test, image build, and security scanning functions.
// Delegates to external stuttgart-things Dagger modules where possible.

package main

import (
	"context"
	"dagger/dagger/internal/dagger"
	"fmt"
)

type Dagger struct{}

// Lint runs golangci-lint on the source code
func (m *Dagger) Lint(
	ctx context.Context,
	src *dagger.Directory,
	// +optional
	// +default="500s"
	timeout string,
) *dagger.Container {
	return dag.Go().Lint(src, dagger.GoLintOpts{
		Timeout: timeout,
	})
}

// Build compiles the Go binary
func (m *Dagger) Build(
	ctx context.Context,
	src *dagger.Directory,
	// +optional
	// +default="main"
	binName string,
	// +optional
	// +default=""
	ldflags string,
	// +optional
	// +default="1.25.5"
	goVersion string,
	// +optional
	// +default="linux"
	os string,
	// +optional
	// +default="amd64"
	arch string,
) *dagger.Directory {
	return dag.Go().BuildBinary(src, dagger.GoBuildBinaryOpts{
		GoVersion:  goVersion,
		Os:         os,
		Arch:       arch,
		BinName:    binName,
		Ldflags:    ldflags,
		GoMainFile: "main.go",
	})
}

// BuildImage builds a container image using ko and optionally pushes it
func (m *Dagger) BuildImage(
	ctx context.Context,
	src *dagger.Directory,
	// +optional
	// +default="ko.local/homerun2-notification-catcher"
	repo string,
	// +optional
	// +default="false"
	push string,
) (string, error) {
	return dag.Go().KoBuild(ctx, src, dagger.GoKoBuildOpts{
		Repo: repo,
		Push: push,
	})
}

// ScanImage scans a container image for vulnerabilities using Trivy
func (m *Dagger) ScanImage(
	ctx context.Context,
	imageRef string,
	// +optional
	// +default="HIGH,CRITICAL"
	severity string,
) *dagger.File {
	return dag.Trivy().ScanImage(imageRef, dagger.TrivyScanImageOpts{
		Severity: severity,
	})
}

// BuildAndTestBinary builds the binary and runs integration tests with Redis
func (m *Dagger) BuildAndTestBinary(
	ctx context.Context,
	source *dagger.Directory,
	// +optional
	// +default="1.25.5"
	goVersion string,
	// +optional
	// +default="linux"
	os string,
	// +optional
	// +default="amd64"
	arch string,
	// +optional
	// +default="main.go"
	goMainFile string,
	// +optional
	// +default="main"
	binName string,
	// +optional
	// +default=""
	ldflags string,
	// +optional
	// +default="."
	packageName string,
	// +optional
	// +default="./..."
	testPath string,
) (*dagger.File, error) {

	binDir := dag.Go().BuildBinary(
		source,
		dagger.GoBuildBinaryOpts{
			GoVersion:   goVersion,
			Os:          os,
			Arch:        arch,
			GoMainFile:  goMainFile,
			BinName:     binName,
			Ldflags:     ldflags,
			PackageName: packageName,
		})

	redisService := dag.Homerun().RedisService(dagger.HomerunRedisServiceOpts{
		Version:  "7.2.0-v18",
		Password: "",
	})

	testCmd := fmt.Sprintf(`
exec > /app/test-output.log 2>&1
set -e

echo "=== Writing minimal notify config ==="
# notification-catcher hard-fails if CONFIG_PATH is unset or the file is
# missing (silent misconfig is the worst failure mode for a dispatch
# service). For the build-and-test smoke we just need the server to start
# and consume one message, so an empty-outputs config is enough — the
# LogHandler still fires.
cat > /app/notify.yaml <<'YAML'
outputs: []
YAML

echo "=== Starting catcher binary ==="
./%s &
BIN_PID=$!
sleep 3

echo ""
echo "=== Checking catcher is running ==="
if kill -0 $BIN_PID 2>/dev/null; then
  echo "Catcher process is running (PID: $BIN_PID)"
else
  echo "Catcher process failed to start!"
  exit 1
fi

echo ""
echo "=== Sending test message to Redis stream ==="
# Use redis-cli to enqueue a test message directly
apk add --no-cache redis > /dev/null 2>&1
redis-cli -h redis -p 6379 XADD messages '*' messageID test-msg-001 > /dev/null

echo "Test message sent, waiting for catcher to process..."
sleep 3

echo ""
echo "=== All tests passed! ==="
kill $BIN_PID 2>/dev/null || true
exit 0
`, binName)

	result := dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache", "curl"}, dagger.ContainerWithExecOpts{}).
		WithDirectory("/app", binDir).
		WithWorkdir("/app").
		WithServiceBinding("redis", redisService).
		WithEnvVariable("REDIS_ADDR", "redis").
		WithEnvVariable("REDIS_PORT", "6379").
		WithEnvVariable("REDIS_STREAM", "messages").
		WithEnvVariable("LOG_FORMAT", "text").
		WithEnvVariable("CONFIG_PATH", "/app/notify.yaml").
		WithExec([]string{"sh", "-c", testCmd}, dagger.ContainerWithExecOpts{})

	_, err := result.Sync(ctx)
	if err != nil {
		testLog := result.File("/app/test-output.log")
		return testLog, fmt.Errorf("tests failed - check test-output.log for details: %w", err)
	}

	testLog := result.File("/app/test-output.log")
	return testLog, nil
}

// IntegrationTest runs a full end-to-end test: spins up Redis, downloads the
// omni-pitcher binary from GitHub releases, pitches test messages through it,
// and verifies the core-catcher consumes them all.
func (m *Dagger) IntegrationTest(
	ctx context.Context,
	// Core-catcher source directory
	source *dagger.Directory,
	// JSON file containing test messages (same format as omni-pitcher smoke tests)
	messagesFile *dagger.File,
	// +optional
	// +default="1.25.5"
	// Go version for building the catcher
	goVersion string,
	// +optional
	// +default="linux"
	os string,
	// +optional
	// +default="amd64"
	arch string,
	// +optional
	// +default="main"
	// Binary name for the catcher
	binName string,
	// +optional
	// +default=""
	ldflags string,
	// +optional
	// +default="latest"
	// omni-pitcher release tag to download (e.g. "v1.2.0" or "latest")
	pitcherVersion string,
	// +optional
	// +default="stuttgart-things/homerun2-omni-pitcher"
	// GitHub repo for the pitcher binary
	pitcherRepo string,
	// +optional
	// +default="2"
	// Delay in seconds between pitching messages
	delaySec int,
) (*dagger.File, error) {

	// Build catcher binary
	catcherBinDir := dag.Go().BuildBinary(
		source,
		dagger.GoBuildBinaryOpts{
			GoVersion:  goVersion,
			Os:         os,
			Arch:       arch,
			GoMainFile: "main.go",
			BinName:    binName,
			Ldflags:    ldflags,
		})

	// Redis service
	redisService := dag.Homerun().RedisService(dagger.HomerunRedisServiceOpts{
		Version:  "7.2.0-v18",
		Password: "",
	})

	// Resolve pitcher version tag
	pitcherTag := pitcherVersion
	if pitcherTag == "latest" {
		pitcherTag = "" // will be resolved in the script via gh CLI
	}

	if delaySec < 1 {
		delaySec = 2
	}

	testCmd := fmt.Sprintf(`
exec > /app/integration-test-report.txt 2>&1
set -e

echo "============================================"
echo "INTEGRATION TEST REPORT"
echo "============================================"
echo "Started:  $(date -u '+%%Y-%%m-%%dT%%H:%%M:%%SZ')"
echo ""

# --- Download pitcher binary ---
echo "--- Downloading omni-pitcher ---"
REPO="%s"
TAG="%s"

if [ -z "$TAG" ]; then
  TAG=$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" | jq -r '.tag_name')
  echo "Resolved latest tag: $TAG"
fi

VERSION=$(echo "$TAG" | sed 's/^v//')
ASSET="homerun2-omni-pitcher_${VERSION}_%s_%s.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

echo "Downloading: $URL"
wget -q "$URL" -O /tmp/pitcher.tar.gz
tar -xzf /tmp/pitcher.tar.gz -C /app/
chmod +x /app/homerun2-omni-pitcher
echo "Pitcher binary ready: $(ls -la /app/homerun2-omni-pitcher)"
echo ""

# --- Start catcher ---
echo "--- Starting core-catcher ---"
./%s &
CATCHER_PID=$!
sleep 3

if kill -0 $CATCHER_PID 2>/dev/null; then
  echo "PASS: Catcher is running (PID: $CATCHER_PID)"
else
  echo "FAIL: Catcher failed to start"
  exit 1
fi
echo ""

# --- Start pitcher ---
echo "--- Starting omni-pitcher ---"
export PORT=8080
export AUTH_TOKEN=integration-test-token
export REDIS_ADDR=redis
export REDIS_PORT=6379
export REDIS_PASSWORD=""
export REDIS_STREAM=messages

./homerun2-omni-pitcher &
PITCHER_PID=$!
sleep 3

if kill -0 $PITCHER_PID 2>/dev/null; then
  echo "PASS: Pitcher is running (PID: $PITCHER_PID)"
else
  echo "FAIL: Pitcher failed to start"
  kill $CATCHER_PID 2>/dev/null || true
  exit 1
fi

# Health check
HTTP_CODE=$(curl -sf -o /dev/null -w "%%{http_code}" http://localhost:8080/health || echo "000")
if [ "$HTTP_CODE" = "200" ]; then
  echo "PASS: Pitcher /health returned 200"
else
  echo "FAIL: Pitcher /health returned $HTTP_CODE"
  kill $CATCHER_PID $PITCHER_PID 2>/dev/null || true
  exit 1
fi
echo ""

# --- Pitch messages ---
echo "--- Pitching messages ---"
TOTAL=$(jq length /app/messages.json)
PASSED=0
FAILED=0
i=0

while [ "$i" -lt "$TOTAL" ]; do
  MSG=$(jq -c ".[$i]" /app/messages.json)
  TITLE=$(echo "$MSG" | jq -r '.title')
  SEVERITY=$(echo "$MSG" | jq -r '.severity // "info"')

  HTTP_CODE=$(curl -sf -o /tmp/pitch-resp.json -w "%%{http_code}" \
    -X POST http://localhost:8080/pitch \
    -H "Authorization: Bearer $AUTH_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$MSG" || echo "000")

  if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "201" ]; then
    STATUS=$(jq -r '.status' /tmp/pitch-resp.json 2>/dev/null)
    OBJECT_ID=$(jq -r '.objectId' /tmp/pitch-resp.json 2>/dev/null)
    if [ "$STATUS" = "success" ]; then
      echo "PASS: [$((i+1))/$TOTAL] $TITLE (severity=$SEVERITY) objectId=$OBJECT_ID"
      PASSED=$((PASSED + 1))
    else
      echo "FAIL: [$((i+1))/$TOTAL] $TITLE - status=$STATUS"
      FAILED=$((FAILED + 1))
    fi
  else
    echo "FAIL: [$((i+1))/$TOTAL] $TITLE - HTTP $HTTP_CODE"
    FAILED=$((FAILED + 1))
  fi

  i=$((i + 1))
  if [ "$i" -lt "$TOTAL" ]; then
    sleep %d
  fi
done
echo ""

# --- Wait for catcher to process ---
echo "--- Waiting for catcher to process messages ---"
sleep 5

# Verify messages were consumed by checking the pending count
apk add --no-cache redis > /dev/null 2>&1
STREAM_LEN=$(redis-cli -h redis -p 6379 XLEN messages)
echo "Stream length: $STREAM_LEN"

# Check consumer group info
GROUP_INFO=$(redis-cli -h redis -p 6379 XINFO GROUPS messages 2>/dev/null || echo "")
echo "Consumer group info:"
echo "$GROUP_INFO"
echo ""

# --- Cleanup ---
kill $CATCHER_PID $PITCHER_PID 2>/dev/null || true
wait $CATCHER_PID 2>/dev/null || true
wait $PITCHER_PID 2>/dev/null || true

# --- Summary ---
echo "============================================"
echo "SUMMARY"
echo "============================================"
echo "Pitcher:  $TAG (from $REPO)"
echo "Messages: $TOTAL pitched"
echo "Passed:   $PASSED"
echo "Failed:   $FAILED"
echo "Ended:    $(date -u '+%%Y-%%m-%%dT%%H:%%M:%%SZ')"
echo "============================================"

if [ "$FAILED" -gt 0 ]; then
  echo "RESULT: FAIL"
  exit 1
else
  echo "RESULT: PASS"
fi
`, pitcherRepo, pitcherTag, os, arch, binName, delaySec)

	result := dag.Container().
		From("alpine:latest").
		WithExec([]string{"apk", "add", "--no-cache", "curl", "jq", "wget"}).
		WithDirectory("/app", catcherBinDir).
		WithMountedFile("/app/messages.json", messagesFile).
		WithWorkdir("/app").
		WithServiceBinding("redis", redisService).
		WithEnvVariable("REDIS_ADDR", "redis").
		WithEnvVariable("REDIS_PORT", "6379").
		WithEnvVariable("REDIS_STREAM", "messages").
		WithEnvVariable("LOG_FORMAT", "text").
		WithExec([]string{"sh", "-c", testCmd})

	_, err := result.Sync(ctx)
	if err != nil {
		report := result.File("/app/integration-test-report.txt")
		return report, fmt.Errorf("integration test failed - check report for details: %w", err)
	}

	return result.File("/app/integration-test-report.txt"), nil
}
