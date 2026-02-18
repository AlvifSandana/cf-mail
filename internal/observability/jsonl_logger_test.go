package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLogger_WritesJSONLAndRedacts(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := NewLogger(buf, NewRedactor([]string{"secret-token"}))
	logger.nowFn = func() time.Time { return time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC) }

	logger.Info("app.start", "starting with secret-token", map[string]any{
		"api_token": "secret-token",
		"safe":      "ok",
	})

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("expected one jsonl line")
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("expected valid json line: %v", err)
	}

	if got["level"] != "info" || got["event"] != "app.start" {
		t.Fatalf("unexpected level/event: %#v", got)
	}
	if msg, _ := got["msg"].(string); strings.Contains(msg, "secret-token") {
		t.Fatalf("expected message redacted, got %q", msg)
	}

	fields, ok := got["fields"].(map[string]any)
	if !ok {
		t.Fatalf("expected fields object")
	}
	if fields["api_token"] != redactedValue {
		t.Fatalf("expected sensitive field redacted, got %#v", fields["api_token"])
	}
	if fields["safe"] != "ok" {
		t.Fatalf("expected safe field preserved, got %#v", fields["safe"])
	}
}

func TestLogger_WarnAndErrorLevels(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := NewLogger(buf, NewRedactor(nil))
	logger.nowFn = func() time.Time { return time.Date(2026, 2, 18, 1, 0, 0, 0, time.UTC) }

	logger.Warn("clipboard.warn", "clipboard unavailable", map[string]any{"safe": "ok"})
	logger.Error("app.err", "runtime failed", nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal first line: %v", err)
	}
	if first["level"] != "warn" || first["event"] != "clipboard.warn" {
		t.Fatalf("unexpected first line: %#v", first)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal second line: %v", err)
	}
	if second["level"] != "error" || second["event"] != "app.err" {
		t.Fatalf("unexpected second line: %#v", second)
	}
}

func TestLogger_FieldsFallbackToStringForUnsupportedType(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := NewLogger(buf, NewRedactor([]string{"unsupported"}))
	logger.nowFn = func() time.Time { return time.Date(2026, 2, 18, 2, 0, 0, 0, time.UTC) }

	logger.Info("bad.fields", "msg", map[string]any{
		"bad": func() {},
	})

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("expected valid json line, got err=%v line=%q", err, strings.TrimSpace(buf.String()))
	}
	fields, ok := got["fields"].(map[string]any)
	if !ok {
		t.Fatalf("expected fields object, got %#v", got["fields"])
	}
	if _, ok := fields["bad"].(string); !ok {
		t.Fatalf("expected unsupported field converted to string, got %#v", fields["bad"])
	}
}

func TestLogger_NilReceiverSafe(t *testing.T) {
	var logger *Logger
	logger.Info("event", "msg", map[string]any{"k": "v"})
	logger.Warn("event", "msg", nil)
	logger.Error("event", "msg", nil)
}

func TestNewLogger_DefaultsNilWriterAndRedactor(t *testing.T) {
	logger := NewLogger(nil, nil)
	if logger == nil {
		t.Fatalf("expected non-nil logger")
	}
	if logger.w == nil {
		t.Fatalf("expected non-nil writer")
	}
	if logger.w != io.Discard {
		t.Fatalf("expected io.Discard writer")
	}
	if logger.redactor == nil {
		t.Fatalf("expected non-nil redactor")
	}
}
