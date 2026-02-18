package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

type fakeClient struct {
	loginErr  error
	selectErr error
	logoutErr error
	closeErr  error
	closeSeq  []error

	loginCalls  int
	selectCalls int
	logoutCalls int
	closeCalls  int

	selectedMailbox  string
	selectedReadOnly bool
	loginUser        string
	loginPass        string

	selectResult MailboxStatus
}

func (f *fakeClient) Login(username, password string) error {
	f.loginCalls++
	f.loginUser = username
	f.loginPass = password
	return f.loginErr
}

func (f *fakeClient) Select(mailbox string, readOnly bool) (MailboxStatus, error) {
	f.selectCalls++
	f.selectedMailbox = mailbox
	f.selectedReadOnly = readOnly
	if f.selectErr != nil {
		return MailboxStatus{}, f.selectErr
	}
	if f.selectResult.Name == "" {
		f.selectResult = MailboxStatus{Name: mailbox, Messages: 1}
	}
	return f.selectResult, nil
}

func (f *fakeClient) Logout() error {
	f.logoutCalls++
	return f.logoutErr
}

func (f *fakeClient) Close() error {
	f.closeCalls++
	if len(f.closeSeq) > 0 {
		err := f.closeSeq[0]
		f.closeSeq = f.closeSeq[1:]
		return err
	}
	return f.closeErr
}

func validConfig() Config {
	return Config{
		Host:           "imap.gmail.com",
		Port:           993,
		Username:       "inbox@example.com",
		Password:       "secret-password",
		Mailbox:        "INBOX",
		ConnectTimeout: 3 * time.Second,
		TLSServerName:  "imap.gmail.com",
	}
}

func TestValidateConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := validConfig()
		if err := ValidateConfig(cfg); err != nil {
			t.Fatalf("ValidateConfig() error = %v", err)
		}
	})

	t.Run("rejects missing fields and insecure tls", func(t *testing.T) {
		cases := []struct {
			name string
			edit func(*Config)
		}{
			{name: "missing host", edit: func(c *Config) { c.Host = "" }},
			{name: "invalid port", edit: func(c *Config) { c.Port = 0 }},
			{name: "missing username", edit: func(c *Config) { c.Username = "" }},
			{name: "missing password", edit: func(c *Config) { c.Password = "" }},
			{name: "missing mailbox", edit: func(c *Config) { c.Mailbox = "" }},
			{name: "invalid timeout", edit: func(c *Config) { c.ConnectTimeout = 0 }},
			{name: "missing tls server name", edit: func(c *Config) { c.TLSServerName = "" }},
			{name: "insecure tls", edit: func(c *Config) { c.InsecureSkipTLS = true }},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := validConfig()
				tc.edit(&cfg)
				if err := ValidateConfig(cfg); err == nil {
					t.Fatalf("expected validation error")
				}
			})
		}
	})
}

func TestConnectAndSelect_FailFastOnInvalidConfig(t *testing.T) {
	called := 0
	_, err := ConnectAndSelect(context.Background(), Config{}, Deps{
		DialTLS: func(context.Context, string, *tls.Config) (Client, error) {
			called++
			return &fakeClient{}, nil
		},
	})
	if err == nil {
		t.Fatalf("expected error for invalid config")
	}
	if called != 0 {
		t.Fatalf("expected no dial call, got %d", called)
	}
}

func TestConnectAndSelect_DialsLoginSelect_Success(t *testing.T) {
	cfg := validConfig()
	fc := &fakeClient{selectResult: MailboxStatus{Name: "INBOX", Messages: 42}}

	dialCalls := 0
	var gotAddr string
	var gotTLS *tls.Config

	conn, err := ConnectAndSelect(context.Background(), cfg, Deps{
		DialTLS: func(_ context.Context, addr string, tlsCfg *tls.Config) (Client, error) {
			dialCalls++
			gotAddr = addr
			gotTLS = tlsCfg
			return fc, nil
		},
	})
	if err != nil {
		t.Fatalf("ConnectAndSelect() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if dialCalls != 1 {
		t.Fatalf("expected one dial call, got %d", dialCalls)
	}
	if gotAddr != "imap.gmail.com:993" {
		t.Fatalf("unexpected dial addr: %q", gotAddr)
	}
	if gotTLS == nil || gotTLS.ServerName != cfg.TLSServerName || gotTLS.InsecureSkipVerify {
		t.Fatalf("unexpected tls config: %+v", gotTLS)
	}
	if fc.loginCalls != 1 || fc.selectCalls != 1 {
		t.Fatalf("expected login/select once, got login=%d select=%d", fc.loginCalls, fc.selectCalls)
	}
	if fc.loginUser != cfg.Username || fc.loginPass != cfg.Password {
		t.Fatalf("unexpected login credentials call: user=%q pass=%q", fc.loginUser, fc.loginPass)
	}
	if fc.selectedMailbox != cfg.Mailbox || fc.selectedReadOnly {
		t.Fatalf("unexpected select args mailbox=%q readonly=%v", fc.selectedMailbox, fc.selectedReadOnly)
	}
	if conn.Mailbox().Name != "INBOX" || conn.Mailbox().Messages != 42 {
		t.Fatalf("unexpected selected mailbox status: %+v", conn.Mailbox())
	}
}

func TestConnectAndSelect_DialFailure(t *testing.T) {
	cfg := validConfig()
	expected := errors.New("dial failed")

	_, err := ConnectAndSelect(context.Background(), cfg, Deps{
		DialTLS: func(_ context.Context, _ string, _ *tls.Config) (Client, error) {
			return nil, expected
		},
	})
	if !errors.Is(err, expected) {
		t.Fatalf("expected wrapped dial error, got %v", err)
	}
}

func TestConnectAndSelect_LoginFailure_CleansUp(t *testing.T) {
	cfg := validConfig()
	loginErr := errors.New("auth failed")
	fc := &fakeClient{loginErr: loginErr}

	_, err := ConnectAndSelect(context.Background(), cfg, Deps{
		DialTLS: func(_ context.Context, _ string, _ *tls.Config) (Client, error) {
			return fc, nil
		},
	})
	if !errors.Is(err, loginErr) {
		t.Fatalf("expected wrapped login error, got %v", err)
	}
	if fc.selectCalls != 0 {
		t.Fatalf("expected no select call after login failure")
	}
	if fc.logoutCalls != 1 || fc.closeCalls != 1 {
		t.Fatalf("expected cleanup logout+close once, got logout=%d close=%d", fc.logoutCalls, fc.closeCalls)
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Fatalf("error must not leak password")
	}
}

func TestConnectAndSelect_SelectFailure_CleansUp(t *testing.T) {
	cfg := validConfig()
	selectErr := errors.New("select failed")
	fc := &fakeClient{selectErr: selectErr}

	_, err := ConnectAndSelect(context.Background(), cfg, Deps{
		DialTLS: func(_ context.Context, _ string, _ *tls.Config) (Client, error) {
			return fc, nil
		},
	})
	if !errors.Is(err, selectErr) {
		t.Fatalf("expected wrapped select error, got %v", err)
	}
	if fc.loginCalls != 1 {
		t.Fatalf("expected login call before select failure")
	}
	if fc.logoutCalls != 1 || fc.closeCalls != 1 {
		t.Fatalf("expected cleanup logout+close once, got logout=%d close=%d", fc.logoutCalls, fc.closeCalls)
	}
}

func TestConnector_Close_Idempotent(t *testing.T) {
	fc := &fakeClient{}
	c := &Connector{client: fc}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() first error = %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() second error = %v", err)
	}

	if fc.logoutCalls != 1 || fc.closeCalls != 1 {
		t.Fatalf("expected one cleanup, got logout=%d close=%d", fc.logoutCalls, fc.closeCalls)
	}
}

func TestConnector_Close_IgnoreAlreadyClosedTransport(t *testing.T) {
	fc := &fakeClient{closeSeq: []error{net.ErrClosed}}
	c := &Connector{client: fc}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() should ignore net.ErrClosed, got %v", err)
	}
	if fc.logoutCalls != 1 || fc.closeCalls != 1 {
		t.Fatalf("expected one cleanup call each, got logout=%d close=%d", fc.logoutCalls, fc.closeCalls)
	}
}

func TestConnectAndSelect_ContextCanceledBeforeDial(t *testing.T) {
	cfg := validConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := 0
	_, err := ConnectAndSelect(ctx, cfg, Deps{
		DialTLS: func(ctx context.Context, _ string, _ *tls.Config) (Client, error) {
			called++
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if called != 1 {
		t.Fatalf("expected one dial attempt, got %d", called)
	}
}
