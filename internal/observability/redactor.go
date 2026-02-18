package observability

import (
	"fmt"
	"reflect"
	"strings"
)

const redactedValue = "[REDACTED]"

type Redactor struct {
	secretValues []string
}

func NewRedactor(secrets []string) *Redactor {
	uniq := make(map[string]struct{})
	vals := make([]string, 0, len(secrets))
	for _, v := range secrets {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		if _, exists := uniq[trimmed]; exists {
			continue
		}
		uniq[trimmed] = struct{}{}
		vals = append(vals, trimmed)
	}

	return &Redactor{secretValues: vals}
}

func (r *Redactor) RedactString(v string) string {
	if r == nil {
		return v
	}

	out := v
	for _, secret := range r.secretValues {
		out = strings.ReplaceAll(out, secret, redactedValue)
	}

	return out
}

func (r *Redactor) RedactFields(fields map[string]any) map[string]any {
	if r == nil {
		out := make(map[string]any, len(fields))
		for k, v := range fields {
			out[k] = v
		}
		return out
	}

	if len(fields) == 0 {
		return map[string]any{}
	}

	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if isSensitiveKey(k) {
			out[k] = redactedValue
			continue
		}
		out[k] = r.redactAny(v)
	}

	return out
}

func (r *Redactor) redactAny(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return t
	case string:
		return r.RedactString(t)
	case error:
		return r.RedactString(t.Error())
	case fmt.Stringer:
		return r.RedactString(t.String())
	case map[string]any:
		return r.RedactFields(t)
	case []string:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, r.RedactString(item))
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, r.redactAny(item))
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, item := range t {
			if isSensitiveKey(k) {
				out[k] = redactedValue
				continue
			}
			out[k] = r.RedactString(item)
		}
		return out
	default:
		return r.redactReflect(reflect.ValueOf(v))
	}
}

func (r *Redactor) redactReflect(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}

	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return r.redactAny(rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return r.RedactString(fmt.Sprintf("%v", rv.Interface()))
		}

		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			if isSensitiveKey(k) {
				out[k] = redactedValue
				continue
			}
			out[k] = r.redactAny(iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, r.redactAny(rv.Index(i).Interface()))
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, rv.NumField())
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := rt.Field(i)
			if f.PkgPath != "" {
				continue
			}

			key := f.Name
			if tag := strings.TrimSpace(f.Tag.Get("json")); tag != "" {
				parts := strings.Split(tag, ",")
				if parts[0] == "-" {
					continue
				}
				if strings.TrimSpace(parts[0]) != "" {
					key = strings.TrimSpace(parts[0])
				}
			}

			if isSensitiveKey(key) {
				out[key] = redactedValue
				continue
			}

			out[key] = r.redactAny(rv.Field(i).Interface())
		}
		return out
	default:
		return r.RedactString(fmt.Sprintf("%v", rv.Interface()))
	}
}

func isSensitiveKey(k string) bool {
	v := strings.ToLower(strings.TrimSpace(k))
	if v == "" {
		return false
	}

	sensitiveParts := []string{"token", "password", "secret", "authorization", "api_key", "apikey", "pass"}
	for _, p := range sensitiveParts {
		if strings.Contains(v, p) {
			return true
		}
	}

	return false
}
