package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/amagioss/opentams/pkg/logger"
)

// --- TC-LOG-01: JSON output has required fields ---

func TestNew_OutputIsJSON(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	l.Info("hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
}

func TestNew_JSONHasRequiredFields(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	l.Info("hello world")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	for _, field := range []string{"time", "level", "msg"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("missing required field %q in log output: %s", field, buf.String())
		}
	}
	if entry["msg"] != "hello world" {
		t.Errorf("msg = %q, want %q", entry["msg"], "hello world")
	}
}

func TestNew_JSONTimeIsRFC3339(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	l.Info("ts check")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	ts, ok := entry["time"].(string)
	if !ok {
		t.Fatalf("time field is not a string: %v", entry["time"])
	}
	// RFC 3339 times contain "T" and "Z" or "+".
	if !strings.Contains(ts, "T") {
		t.Errorf("time field %q does not look like RFC 3339", ts)
	}
}

// --- TC-LOG-02: Level filtering ---

func TestNew_LevelFiltering_DebugSuppressedAtInfo(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	l.Debug("should not appear")

	if buf.Len() != 0 {
		t.Errorf("DEBUG message was not suppressed at INFO level: %s", buf.String())
	}
}

func TestNew_LevelFiltering_InfoPassesAtInfo(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	l.Info("should appear")

	if buf.Len() == 0 {
		t.Error("INFO message was suppressed at INFO level")
	}
}

func TestNew_LevelFiltering_WarnSuppressedAtError(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.ErrorLevel)
	l.Warn("should not appear")

	if buf.Len() != 0 {
		t.Errorf("WARN message was not suppressed at ERROR level: %s", buf.String())
	}
}

func TestNew_LevelFiltering_AllLevelsPassAtDebug(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.DebugLevel)
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Errorf("expected 4 log lines at DEBUG level, got %d:\n%s", len(lines), buf.String())
	}
}

// --- TC-LOG-03: Runtime level switching via AtomicLevel ---

func TestAtomicLevel_SwitchSuppressesLowerLevel(t *testing.T) {
	var buf bytes.Buffer
	l, atom := logger.New(&buf, zapcore.DebugLevel)
	l.Debug("visible before switch")

	buf.Reset()
	atom.SetLevel(zapcore.WarnLevel)
	l.Info("should be suppressed after switch to WARN")

	if buf.Len() != 0 {
		t.Errorf("INFO message not suppressed after switching level to WARN: %s", buf.String())
	}
}

func TestAtomicLevel_SwitchUnblocksPreviouslySuppressed(t *testing.T) {
	var buf bytes.Buffer
	l, atom := logger.New(&buf, zapcore.ErrorLevel)

	atom.SetLevel(zapcore.DebugLevel)
	l.Debug("visible after lowering level")

	if buf.Len() == 0 {
		t.Error("DEBUG message suppressed even after lowering level to DEBUG")
	}
}

// --- TC-LOG-04: ParseLevel valid inputs ---

func TestParseLevel_ValidInputs(t *testing.T) {
	cases := []struct {
		input string
		want  zapcore.Level
	}{
		{"debug", zapcore.DebugLevel},
		{"info", zapcore.InfoLevel},
		{"warn", zapcore.WarnLevel},
		{"error", zapcore.ErrorLevel},
		{"DEBUG", zapcore.DebugLevel},
		{"INFO", zapcore.InfoLevel},
		{"WARN", zapcore.WarnLevel},
		{"ERROR", zapcore.ErrorLevel},
	}
	for _, tc := range cases {
		got, err := logger.ParseLevel(tc.input)
		if err != nil {
			t.Errorf("ParseLevel(%q) returned unexpected error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// --- TC-LOG-05: ParseLevel invalid input ---

func TestParseLevel_InvalidInput(t *testing.T) {
	invalids := []string{"", "verbose", "trace", "fatal", "notice", "INFO2"}
	for _, s := range invalids {
		_, err := logger.ParseLevel(s)
		if err == nil {
			t.Errorf("ParseLevel(%q) expected error, got nil", s)
		}
	}
}

// --- TC-LOG-06: No sampling — every message must be emitted ---

func TestNew_NoSampling(t *testing.T) {
	const count = 1000
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)

	for i := 0; i < count; i++ {
		l.Info("same message repeated")
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != count {
		t.Errorf("sampling detected: logged %d lines, expected %d (all messages must be emitted)", len(lines), count)
	}
}

// --- TC-LOG-07: Goroutine safety — concurrent writes + level switch ---

func TestNew_ConcurrentSafety(t *testing.T) {
	var buf syncBuffer
	l, atom := logger.New(&buf, zapcore.InfoLevel)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Info("concurrent message")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, lvl := range []zapcore.Level{zapcore.DebugLevel, zapcore.WarnLevel, zapcore.InfoLevel} {
			atom.SetLevel(lvl)
		}
	}()
	wg.Wait()
	// Run with: go test -race ./pkg/logger/... — no assertions needed, race detector is the test.
}

// --- TC-LOG-08: WithSampling drops messages above the threshold ---

func TestWithSampling_DropsMessagesAboveThreshold(t *testing.T) {
	const (
		initial    = 3
		thereafter = 1000 // only every 1000th message after the first 3
		total      = 20
	)
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel, logger.WithSampling(initial, thereafter))

	for i := 0; i < total; i++ {
		l.Info("sampled message")
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) >= total {
		t.Errorf("sampling not active: got %d lines, expected fewer than %d", len(lines), total)
	}
	if len(lines) < initial {
		t.Errorf("sampling too aggressive: got %d lines, expected at least %d (initial threshold)", len(lines), initial)
	}
}

// --- TC-LOG-09: WithCaller option adds file:line field ---

func TestWithCaller_FieldPresent(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel, logger.WithCaller())
	l.Info("test")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if _, ok := entry["caller"]; !ok {
		t.Errorf("caller field missing with WithCaller option: %s", buf.String())
	}
}

func TestWithoutCaller_FieldAbsent(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel) // no WithCaller
	l.Info("test")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if _, ok := entry["caller"]; ok {
		t.Errorf("caller field present without WithCaller option: %s", buf.String())
	}
}

// --- TC-LOG-09: WithStacktrace option adds stack trace at and above threshold ---

func TestWithStacktrace_PresentAtThresholdLevel(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.DebugLevel, logger.WithStacktrace(zapcore.ErrorLevel))
	l.Error("oops")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if _, ok := entry["stacktrace"]; !ok {
		t.Errorf("stacktrace field missing at Error level: %s", buf.String())
	}
}

func TestWithStacktrace_AbsentBelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.DebugLevel, logger.WithStacktrace(zapcore.ErrorLevel))
	l.Warn("just a warning")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if _, ok := entry["stacktrace"]; ok {
		t.Errorf("stacktrace field present at Warn level when threshold is Error: %s", buf.String())
	}
}

// --- TC-LOG-10: Context helpers ---

func TestWithContext_FromContext_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l, _ := logger.New(&buf, zapcore.InfoLevel)
	ctx := logger.WithContext(context.Background(), l)

	got := logger.FromContext(ctx)
	got.Info("via context")

	if buf.Len() == 0 {
		t.Error("logger retrieved from context did not write output")
	}
}

func TestFromContext_ReturnsNopWhenEmpty(t *testing.T) {
	got := logger.FromContext(context.Background())
	// Must not panic; nop logger silently discards output.
	got.Info("nop message")
}

func TestFromContext_UsesFallbackWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	fallback, _ := logger.New(&buf, zapcore.InfoLevel)

	got := logger.FromContext(context.Background(), fallback)
	got.Info("from fallback")

	if buf.Len() == 0 {
		t.Error("fallback logger not used when context has no logger")
	}
}

func TestFromContext_PrefersContextOverFallback(t *testing.T) {
	var ctxBuf, fallbackBuf bytes.Buffer
	ctxLogger, _ := logger.New(&ctxBuf, zapcore.InfoLevel)
	fallback, _ := logger.New(&fallbackBuf, zapcore.InfoLevel)

	ctx := logger.WithContext(context.Background(), ctxLogger)
	got := logger.FromContext(ctx, fallback)
	got.Info("should use context logger")

	if ctxBuf.Len() == 0 {
		t.Error("context logger not used when present")
	}
	if fallbackBuf.Len() != 0 {
		t.Error("fallback logger used even though context logger was present")
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Sync() error { return nil }
