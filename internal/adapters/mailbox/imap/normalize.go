package imap

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const defaultSnippetMax = 280

const (
	maxToEntries      = 64
	maxAddressLen     = 320
	maxSubjectBytes   = 1024
	maxMessageIDBytes = 255
	maxSnippetBytes   = 4096
	maxBodyBytes      = 64 * 1024
)

type IncomingEmail struct {
	To         []string
	From       string
	Subject    string
	MessageID  string
	Snippet    string
	Body       string
	ReceivedAt time.Time
}

func NormalizeIncomingEmail(in IncomingEmail) (IncomingEmail, error) {
	if len(in.To) > maxToEntries {
		return IncomingEmail{}, fmt.Errorf("too many to addresses")
	}
	if len(in.From) > maxAddressLen {
		return IncomingEmail{}, fmt.Errorf("from address too long")
	}

	in.Subject = truncateBytes(in.Subject, maxSubjectBytes)
	in.MessageID = truncateBytes(in.MessageID, maxMessageIDBytes)
	in.Snippet = truncateBytes(in.Snippet, maxSnippetBytes)
	in.Body = truncateBytes(in.Body, maxBodyBytes)

	to, err := normalizeAddressList(in.To)
	if err != nil {
		return IncomingEmail{}, fmt.Errorf("normalize to addresses: %w", err)
	}
	if len(to) == 0 {
		return IncomingEmail{}, fmt.Errorf("to address is required")
	}

	from, err := normalizeOptionalAddress(in.From)
	if err != nil {
		return IncomingEmail{}, fmt.Errorf("normalize from address: %w", err)
	}

	body := normalizeText(in.Body)
	subject := normalizeText(in.Subject)
	messageID := normalizeMessageID(in.MessageID)

	snippet := normalizeText(in.Snippet)
	if snippet == "" {
		snippet = deriveSnippet(body, defaultSnippetMax)
	} else {
		snippet = limitRunes(snippet, defaultSnippetMax)
	}

	receivedAt := in.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}

	return IncomingEmail{
		To:         to,
		From:       from,
		Subject:    subject,
		MessageID:  messageID,
		Snippet:    snippet,
		Body:       body,
		ReceivedAt: receivedAt,
	}, nil
}

func normalizeAddressList(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, raw := range values {
		if len(raw) > maxAddressLen {
			return nil, fmt.Errorf("address too long")
		}

		items := splitAddresses(raw)
		for _, item := range items {
			if len(item) > maxAddressLen {
				return nil, fmt.Errorf("address too long")
			}

			addr, err := normalizeAddress(item)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[addr]; exists {
				continue
			}
			seen[addr] = struct{}{}
			out = append(out, addr)
			if len(out) > maxToEntries {
				return nil, fmt.Errorf("too many to addresses")
			}
		}
	}

	return out, nil
}

func splitAddresses(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parsed, err := mail.ParseAddressList(raw)
	if err == nil {
		out := make([]string, 0, len(parsed))
		for _, a := range parsed {
			if a == nil {
				continue
			}
			out = append(out, a.Address)
		}
		return out
	}

	return []string{raw}
}

func normalizeOptionalAddress(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}

	return normalizeAddress(v)
}

func normalizeAddress(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("email is required")
	}

	parsed, err := mail.ParseAddress(v)
	if err != nil {
		return "", err
	}

	return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
}

func normalizeMessageID(v string) string {
	v = truncateBytes(v, maxMessageIDBytes)
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, v)
	v = strings.TrimSpace(v)
	return strings.ToLower(v)
}

func normalizeText(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	v = strings.TrimSpace(v)
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)

	lines := strings.Split(v, "\n")
	for i := range lines {
		lines[i] = compactWhitespace(lines[i])
	}

	v = strings.Join(lines, "\n")
	v = strings.TrimSpace(v)
	return v
}

func compactWhitespace(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	fields := strings.Fields(v)
	return strings.Join(fields, " ")
}

func deriveSnippet(body string, maxRunes int) string {
	if body == "" {
		return ""
	}

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return limitRunes(line, maxRunes)
	}

	return limitRunes(body, maxRunes)
}

func limitRunes(v string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}

	runes := []rune(v)
	if len(runes) <= maxRunes {
		return v
	}

	return string(runes[:maxRunes])
}

func truncateBytes(v string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(v) <= maxBytes {
		return v
	}
	// Walk back to valid UTF-8 rune boundary to avoid cutting
	// in the middle of a multi-byte character.
	for maxBytes > 0 && !utf8.RuneStart(v[maxBytes]) {
		maxBytes--
	}
	return v[:maxBytes]
}
