package parser

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNoRuleMatched = errors.New("no parser rule matched")
	ErrNoOTPFound    = errors.New("no otp found for matched rule")
	ErrAliasRequired = errors.New("incoming email alias recipient is required")
)

type Rule struct {
	Platform     string
	FromContains []string
	SubjectRegex string
	OTPRegex     string
}

type IncomingEmail struct {
	To         []string
	From       string
	Subject    string
	MessageID  string
	Snippet    string
	Body       string
	ReceivedAt time.Time
}

type ParsedOTP struct {
	Platform   string
	OTPCode    string
	AliasEmail string
	MessageID  string
	Subject    string
	FromEmail  string
	Snippet    string
	ReceivedAt time.Time
}

type Engine struct {
	rules []compiledRule
}

type compiledRule struct {
	platform     string
	fromContains []string
	subjectRegex *regexp.Regexp
	otpRegex     *regexp.Regexp
}

func NewEngine(rules []Rule) (*Engine, error) {
	compiled := make([]compiledRule, 0, len(rules))
	for i, r := range rules {
		platform := strings.ToUpper(strings.TrimSpace(r.Platform))
		if platform == "" {
			return nil, fmt.Errorf("rule[%d] platform is required", i)
		}

		if strings.TrimSpace(r.OTPRegex) == "" {
			return nil, fmt.Errorf("rule[%d] otp_regex is required", i)
		}

		otpRe, err := regexp.Compile(r.OTPRegex)
		if err != nil {
			return nil, fmt.Errorf("compile rule[%d] otp_regex: %w", i, err)
		}

		var subjectRe *regexp.Regexp
		if strings.TrimSpace(r.SubjectRegex) != "" {
			subjectRe, err = regexp.Compile(r.SubjectRegex)
			if err != nil {
				return nil, fmt.Errorf("compile rule[%d] subject_regex: %w", i, err)
			}
		}

		contains := make([]string, 0, len(r.FromContains))
		for _, item := range r.FromContains {
			t := strings.ToLower(strings.TrimSpace(item))
			if t == "" {
				continue
			}
			contains = append(contains, t)
		}

		compiled = append(compiled, compiledRule{
			platform:     platform,
			fromContains: contains,
			subjectRegex: subjectRe,
			otpRegex:     otpRe,
		})
	}

	return &Engine{rules: compiled}, nil
}

func (e *Engine) Parse(in IncomingEmail) (ParsedOTP, error) {
	if e == nil {
		return ParsedOTP{}, fmt.Errorf("parser engine is nil")
	}
	if len(e.rules) == 0 {
		return ParsedOTP{}, ErrNoRuleMatched
	}

	aliasEmail := extractAliasEmail(in.To)
	if aliasEmail == "" {
		return ParsedOTP{}, ErrAliasRequired
	}

	fromLower := normalizeSenderAddress(in.From)
	matchedRule := false

	for _, r := range e.rules {
		if !matchFromContains(fromLower, r.fromContains) {
			continue
		}

		if r.subjectRegex != nil && !r.subjectRegex.MatchString(in.Subject) {
			continue
		}

		matchedRule = true
		otp := extractOTP(r.otpRegex, in.Subject, in.Body, in.Snippet)
		if otp == "" {
			continue
		}

		receivedAt := in.ReceivedAt
		if !receivedAt.IsZero() {
			receivedAt = receivedAt.UTC()
		}

		return ParsedOTP{
			Platform:   r.platform,
			OTPCode:    otp,
			AliasEmail: aliasEmail,
			MessageID:  in.MessageID,
			Subject:    in.Subject,
			FromEmail:  in.From,
			Snippet:    in.Snippet,
			ReceivedAt: receivedAt,
		}, nil
	}

	if matchedRule {
		return ParsedOTP{}, ErrNoOTPFound
	}

	return ParsedOTP{}, ErrNoRuleMatched
}

func matchFromContains(fromLower string, contains []string) bool {
	fromLower = strings.ToLower(strings.TrimSpace(fromLower))
	if fromLower == "" {
		return len(contains) == 0
	}

	if len(contains) == 0 {
		return true
	}

	parts := strings.SplitN(fromLower, "@", 2)
	if len(parts) != 2 {
		return false
	}
	senderLocal := parts[0]
	senderDomain := parts[1]

	hasSecureToken := false
	for _, item := range contains {
		token := strings.ToLower(strings.TrimSpace(item))
		if token == "" {
			continue
		}
		if strings.Contains(token, "@") || strings.Contains(token, ".") {
			hasSecureToken = true
			break
		}
	}

	for _, item := range contains {
		token := strings.ToLower(strings.TrimSpace(item))
		if token == "" {
			continue
		}

		if strings.Contains(token, "@") {
			if fromLower == token {
				return true
			}
			continue
		}

		if strings.Contains(token, ".") {
			token = strings.TrimPrefix(token, "@")
			if token == "" {
				continue
			}
			if senderDomain == token || strings.HasSuffix(senderDomain, "."+token) {
				return true
			}
			continue
		}

		token = strings.TrimPrefix(token, "@")
		if token == "" {
			continue
		}
		if hasSecureToken {
			continue
		}

		if senderLocal == token {
			return true
		}
	}

	return false
}

func extractAliasEmail(to []string) string {
	for _, item := range to {
		v := strings.ToLower(strings.TrimSpace(item))
		if v != "" {
			return v
		}
	}
	return ""
}

func normalizeSenderAddress(from string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return ""
	}

	parsed, err := mail.ParseAddress(from)
	if err == nil {
		return strings.ToLower(strings.TrimSpace(parsed.Address))
	}

	return strings.ToLower(from)
}

func extractOTP(re *regexp.Regexp, fields ...string) string {
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		match := re.FindStringSubmatch(field)
		if len(match) == 0 {
			continue
		}

		if len(match) > 1 {
			for i := 1; i < len(match); i++ {
				if v := strings.TrimSpace(match[i]); v != "" {
					return v
				}
			}
		}

		if v := strings.TrimSpace(match[0]); v != "" {
			return v
		}
	}

	return ""
}
