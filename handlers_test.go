package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHoldAnswersAfterTheRequestedTime(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	start := time.Now()
	resp := get(t, server.URL+"/hold/0.2")
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("answered after %s, want at least 200ms", elapsed)
	}
	var result holdResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "hold" || result.Requested != "200ms" {
		t.Fatalf("result = %+v, want mode hold and requested 200ms", result)
	}
}

func TestHoldAcceptsGoDurations(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	resp := get(t, server.URL+"/hold/150ms")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHoldRejectsDurationsItCannotUse(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	for _, duration := range []string{"abc", "-1", "-5s", "NaN", "Inf", "2m", "61"} {
		t.Run(duration, func(t *testing.T) {
			resp := get(t, server.URL+"/hold/"+duration)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestHoldLogsWhenTheCallerGivesUp(t *testing.T) {
	server, logs := newTestServer(t, config{maxHold: time.Minute})
	client := &http.Client{Timeout: 100 * time.Millisecond}

	_, err := client.Get(server.URL + "/hold/30?probe=impatient")

	if err == nil {
		t.Fatal("expected the client to time out")
	}
	waitForLog(t, logs, "caller gave up")
	if !strings.Contains(logs.String(), "probe=impatient") {
		t.Fatalf("log does not name the probe:\n%s", logs.String())
	}
}

func TestDripSendsHeadersRightAwayAndThenOneLinePerInterval(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	start := time.Now()
	resp := get(t, server.URL+"/drip/1?interval=100ms")
	headersAfter := time.Since(start)

	if headersAfter > 500*time.Millisecond {
		t.Fatalf("headers arrived after %s, want them right away", headersAfter)
	}
	lines := readLines(t, resp.Body)
	if stillHere := countPrefix(lines, "still here after"); stillHere < 5 {
		t.Fatalf("got %d still-here lines, want at least 5:\n%s", stillHere, strings.Join(lines, "\n"))
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "done after") {
		t.Fatalf("last line = %q, want it to start with done after", last)
	}
}

func TestDripWithALongIntervalSendsNothingUntilDone(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	resp := get(t, server.URL+"/drip/0.3?interval=1h")

	lines := readLines(t, resp.Body)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "done after") {
		t.Fatalf("lines = %q, want only the done line", lines)
	}
}

func TestDripRejectsAZeroInterval(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute})

	resp := get(t, server.URL+"/drip/1?interval=0")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestBasePathIsRemovedBeforeRouting(t *testing.T) {
	server, _ := newTestServer(t, config{maxHold: time.Minute, basePath: "/core/gateway-timeout"})

	if resp := get(t, server.URL+"/core/gateway-timeout/hold/0"); resp.StatusCode != http.StatusOK {
		t.Fatalf("with base path: status = %d, want 200", resp.StatusCode)
	}
	if resp := get(t, server.URL+"/hold/0"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("without base path: status = %d, want 404", resp.StatusCode)
	}
}

func TestLoadConfigUsesDefaultsWhenNothingIsSet(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	if err != nil {
		t.Fatal(err)
	}
	want := config{port: "8080", maxHold: 30 * time.Minute}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigReadsEveryVariable(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PORT":      "9090",
		"BASE_PATH": "/gateway-timeout/",
		"MAX_HOLD":  "1h",
	}))

	if err != nil {
		t.Fatal(err)
	}
	want := config{port: "9090", basePath: "/gateway-timeout", maxHold: time.Hour}
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"base path without leading slash": {"BASE_PATH": "gateway-timeout"},
		"max hold that is not a duration": {"MAX_HOLD": "forever"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadConfig(env(vars)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestServer(t *testing.T, cfg config) (*httptest.Server, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	server := httptest.NewServer(newHandler(cfg, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(server.Close)
	return server, logs
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readLines(t *testing.T, body io.Reader) []string {
	t.Helper()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

func countPrefix(lines []string, prefix string) int {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func waitForLog(t *testing.T, logs *logBuffer, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), message) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log never said %q:\n%s", message, logs.String())
}

func env(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}
