package parser

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

const defaultOutputTemplate = "{{.Platform}} | {{.OTP}} | {{.ReceivedAt}} | {{.Alias}}"

type Renderer struct {
	tmpl *template.Template
}

func NewRenderer(format string) (*Renderer, error) {
	format = strings.TrimSpace(format)
	if format == "" {
		format = defaultOutputTemplate
	}

	tmpl, err := template.New("otp_output").Option("missingkey=error").Parse(format)
	if err != nil {
		return nil, fmt.Errorf("parse output template: %w", err)
	}

	return &Renderer{tmpl: tmpl}, nil
}

func (r *Renderer) Render(in ParsedOTP) (string, error) {
	if r == nil || r.tmpl == nil {
		return "", fmt.Errorf("renderer is nil")
	}

	receivedAt := in.ReceivedAt
	receivedAtStr := ""
	if !receivedAt.IsZero() {
		receivedAt = receivedAt.UTC()
		receivedAtStr = receivedAt.Format(time.RFC3339)
	}

	data := struct {
		Platform      string
		OTP           string
		OTPCode       string
		Alias         string
		AliasEmail    string
		MessageID     string
		Subject       string
		From          string
		FromEmail     string
		Snippet       string
		ReceivedAt    string
		ReceivedAtRaw time.Time
	}{
		Platform:      in.Platform,
		OTP:           in.OTPCode,
		OTPCode:       in.OTPCode,
		Alias:         in.AliasEmail,
		AliasEmail:    in.AliasEmail,
		MessageID:     in.MessageID,
		Subject:       in.Subject,
		From:          in.FromEmail,
		FromEmail:     in.FromEmail,
		Snippet:       in.Snippet,
		ReceivedAt:    receivedAtStr,
		ReceivedAtRaw: receivedAt,
	}

	var b strings.Builder
	if err := r.tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render output template: %w", err)
	}

	return strings.TrimSpace(b.String()), nil
}
