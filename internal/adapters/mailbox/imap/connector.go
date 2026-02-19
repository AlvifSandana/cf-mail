package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	goimap "github.com/emersion/go-imap"
	goimapclient "github.com/emersion/go-imap/client"
)

type Config struct {
	Host            string
	Port            int
	Username        string
	Password        string
	Mailbox         string
	ConnectTimeout  time.Duration
	TLSServerName   string
	InsecureSkipTLS bool
}

type MailboxStatus struct {
	Name     string
	Messages uint32
}

type Client interface {
	Login(username, password string) error
	Select(mailbox string, readOnly bool) (MailboxStatus, error)
	Logout() error
	Close() error
}

type Deps struct {
	DialTLS func(ctx context.Context, addr string, tlsCfg *tls.Config) (Client, error)
}

type Connector struct {
	client  Client
	mailbox MailboxStatus

	closeOnce sync.Once
	closeErr  error
}

const defaultPollTimeout = 15 * time.Second

func ValidateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("imap.host is required")
	}
	if cfg.Port <= 0 {
		return fmt.Errorf("imap.port must be > 0")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return fmt.Errorf("imap.username is required")
	}
	if cfg.Password == "" {
		return fmt.Errorf("imap password is empty")
	}
	if strings.TrimSpace(cfg.Mailbox) == "" {
		return fmt.Errorf("imap.mailbox is required")
	}
	if cfg.ConnectTimeout <= 0 {
		return fmt.Errorf("imap connect timeout must be > 0")
	}
	if strings.TrimSpace(cfg.TLSServerName) == "" {
		return fmt.Errorf("imap tls server name is required")
	}
	if cfg.InsecureSkipTLS {
		return fmt.Errorf("imap insecure tls is not allowed")
	}

	return nil
}

func ConnectAndSelect(ctx context.Context, cfg Config, deps Deps) (*Connector, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("validate imap config: %w", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	opCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	dialTLS := deps.DialTLS
	if dialTLS == nil {
		dialTLS = defaultDialTLS
	}

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         strings.TrimSpace(cfg.TLSServerName),
		InsecureSkipVerify: false,
	}
	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(cfg.Port))

	client, err := dialTLS(opCtx, addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("dial imap over tls: %w", err)
	}

	if err := callWithContext(opCtx, func() error {
		return client.Login(cfg.Username, cfg.Password)
	}, func() {
		_ = client.Close()
	}); err != nil {
		if cleanupErr := cleanupClient(client); cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("login imap: %w", err), fmt.Errorf("cleanup imap session: %w", cleanupErr))
		}
		return nil, fmt.Errorf("login imap: %w", err)
	}

	selected, err := selectWithContext(opCtx, func() (MailboxStatus, error) {
		return client.Select(cfg.Mailbox, false)
	}, func() {
		_ = client.Close()
	})
	if err != nil {
		if cleanupErr := cleanupClient(client); cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("select imap mailbox: %w", err), fmt.Errorf("cleanup imap session: %w", cleanupErr))
		}
		return nil, fmt.Errorf("select imap mailbox: %w", err)
	}

	return &Connector{client: client, mailbox: selected}, nil
}

func (c *Connector) Mailbox() MailboxStatus {
	if c == nil {
		return MailboxStatus{}
	}
	return c.mailbox
}

// Poll checks mailbox session health by issuing a lightweight select
// against the currently selected mailbox.
func (c *Connector) Poll(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("imap connector is nil")
	}
	if c.client == nil {
		return fmt.Errorf("imap connector client is nil")
	}
	if strings.TrimSpace(c.mailbox.Name) == "" {
		return fmt.Errorf("imap connector mailbox is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pollCtx, cancel := context.WithTimeout(ctx, defaultPollTimeout)
	defer cancel()

	return callWithContext(pollCtx, func() error {
		_, err := c.client.Select(c.mailbox.Name, false)
		return err
	}, func() {
		_ = c.client.Close()
	})
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		c.closeErr = cleanupClient(c.client)
	})

	return c.closeErr
}

func cleanupClient(c Client) error {
	if c == nil {
		return nil
	}

	var errs []error
	if err := c.Logout(); err != nil {
		errs = append(errs, fmt.Errorf("logout imap: %w", err))
	}
	if err := c.Close(); err != nil {
		if !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close imap connection: %w", err))
		}
	}

	return errors.Join(errs...)
}

func callWithContext(ctx context.Context, fn func() error, onCancel func()) error {
	type result struct {
		err error
	}

	ch := make(chan result, 1)
	go func() {
		ch <- result{err: fn()}
	}()

	select {
	case <-ctx.Done():
		if onCancel != nil {
			onCancel()
		}
		return ctx.Err()
	case r := <-ch:
		return r.err
	}
}

func selectWithContext(ctx context.Context, fn func() (MailboxStatus, error), onCancel func()) (MailboxStatus, error) {
	type result struct {
		status MailboxStatus
		err    error
	}

	ch := make(chan result, 1)
	go func() {
		status, err := fn()
		ch <- result{status: status, err: err}
	}()

	select {
	case <-ctx.Done():
		if onCancel != nil {
			onCancel()
		}
		return MailboxStatus{}, ctx.Err()
	case r := <-ch:
		return r.status, r.err
	}
}

func defaultDialTLS(ctx context.Context, addr string, tlsCfg *tls.Config) (Client, error) {
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{},
		Config:    tlsCfg,
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	client, err := goimapclient.New(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &goIMAPClient{inner: client}, nil
}

type goIMAPClient struct {
	inner *goimapclient.Client
}

func (c *goIMAPClient) Login(username, password string) error {
	return c.inner.Login(username, password)
}

func (c *goIMAPClient) Select(mailbox string, readOnly bool) (MailboxStatus, error) {
	status, err := c.inner.Select(mailbox, readOnly)
	if err != nil {
		return MailboxStatus{}, err
	}
	if status == nil {
		return MailboxStatus{}, fmt.Errorf("imap select returned empty mailbox status")
	}

	return mapMailboxStatus(status), nil
}

func (c *goIMAPClient) Logout() error {
	return c.inner.Logout()
}

func (c *goIMAPClient) Close() error {
	return c.inner.Terminate()
}

func mapMailboxStatus(s *goimap.MailboxStatus) MailboxStatus {
	if s == nil {
		return MailboxStatus{}
	}
	return MailboxStatus{Name: s.Name, Messages: s.Messages}
}
