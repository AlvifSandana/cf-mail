package observability

import "testing"

func TestRedactor_RedactStringBySecretValue(t *testing.T) {
	r := NewRedactor([]string{"top-secret-token"})

	got := r.RedactString("token=top-secret-token")
	if got != "token=[REDACTED]" {
		t.Fatalf("unexpected redacted output: %q", got)
	}
}

func TestRedactor_RedactFieldsBySensitiveKey(t *testing.T) {
	r := NewRedactor(nil)
	out := r.RedactFields(map[string]any{
		"api_token": "abc",
		"password":  "def",
		"safe":      "ok",
	})

	if out["api_token"] != redactedValue || out["password"] != redactedValue {
		t.Fatalf("expected sensitive keys redacted: %#v", out)
	}
	if out["safe"] != "ok" {
		t.Fatalf("expected safe field unchanged, got %#v", out["safe"])
	}
}

func TestRedactor_RedactNestedFields(t *testing.T) {
	r := NewRedactor([]string{"imap-pass"})
	out := r.RedactFields(map[string]any{
		"nested": map[string]any{
			"value": "imap-pass",
		},
	})

	nested, ok := out["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map output")
	}
	if nested["value"] != redactedValue {
		t.Fatalf("expected nested value redacted, got %#v", nested["value"])
	}
}

func TestRedactor_RedactTypedMapAndStructByKey(t *testing.T) {
	type payload struct {
		AccessToken string `json:"access_token"`
		Safe        string `json:"safe"`
	}

	r := NewRedactor([]string{"value-secret"})
	out := r.RedactFields(map[string]any{
		"typed_map": map[string]string{
			"api_key": "abc",
			"safe":    "value-secret",
		},
		"typed_struct": payload{AccessToken: "abc", Safe: "value-secret"},
	})

	m, ok := out["typed_map"].(map[string]any)
	if !ok {
		t.Fatalf("expected typed map converted to map[string]any, got %T", out["typed_map"])
	}
	if m["api_key"] != redactedValue {
		t.Fatalf("expected api_key redacted, got %#v", m["api_key"])
	}
	if m["safe"] != redactedValue {
		t.Fatalf("expected secret value redacted, got %#v", m["safe"])
	}

	s, ok := out["typed_struct"].(map[string]any)
	if !ok {
		t.Fatalf("expected typed struct converted to map[string]any, got %T", out["typed_struct"])
	}
	if s["access_token"] != redactedValue {
		t.Fatalf("expected struct access_token redacted, got %#v", s["access_token"])
	}
	if s["safe"] != redactedValue {
		t.Fatalf("expected struct safe value redacted, got %#v", s["safe"])
	}
}
