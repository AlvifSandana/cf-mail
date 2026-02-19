package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap"
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

	uidSearchResult []uint32
	uidSearchErr    error
	uidFetchErr     error
	fetchedMessages []*goimap.Message
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

func (f *fakeClient) UidSearch(_ *goimap.SearchCriteria) ([]uint32, error) {
	if f.uidSearchErr != nil {
		return nil, f.uidSearchErr
	}
	out := make([]uint32, len(f.uidSearchResult))
	copy(out, f.uidSearchResult)
	return out, nil
}

func (f *fakeClient) UidFetch(_ *goimap.SeqSet, _ []goimap.FetchItem, ch chan *goimap.Message) error {
	defer close(ch)
	if f.uidFetchErr != nil {
		return f.uidFetchErr
	}
	for _, msg := range f.fetchedMessages {
		ch <- msg
	}
	return nil
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

type stringLiteral struct{ r *strings.Reader }

func (l stringLiteral) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l stringLiteral) Len() int                   { return l.r.Len() }

func TestConnector_PollUpdates_FetchesAndNormalizesIncomingEmail(t *testing.T) {
	section := &goimap.BodySectionName{Peek: true, Partial: []int{0, maxBodyBytes * 2}}
	body := "From: noreply@tm.openai.com\r\nTo: alias@example.com\r\nSubject: Your ChatGPT code is 339322\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nEnter this temporary verification code to continue: 339322"

	msg := goimap.NewMessage(1, []goimap.FetchItem{goimap.FetchUid, goimap.FetchEnvelope, goimap.FetchInternalDate, section.FetchItem()})
	msg.Uid = 10
	msg.Envelope = &goimap.Envelope{
		Subject:   "Your ChatGPT code is 339322",
		MessageId: "<msg-1@example.com>",
		From:      []*goimap.Address{{MailboxName: "noreply", HostName: "tm.openai.com"}},
		To:        []*goimap.Address{{MailboxName: "alias", HostName: "example.com"}},
	}
	msg.InternalDate = time.Date(2026, 2, 19, 2, 36, 0, 0, time.UTC)
	msg.Body = map[*goimap.BodySectionName]goimap.Literal{section: stringLiteral{r: strings.NewReader(body)}}

	fc := &fakeClient{
		selectResult:    MailboxStatus{Name: "INBOX", Messages: 1},
		uidSearchResult: []uint32{10},
		fetchedMessages: []*goimap.Message{msg},
	}

	c := &Connector{client: fc, mailbox: MailboxStatus{Name: "INBOX", Messages: 1}}

	updates, err := c.PollUpdates(context.Background())
	if err != nil {
		t.Fatalf("PollUpdates() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected one incoming email update, got %d", len(updates))
	}

	in := updates[0]
	if len(in.To) != 1 || in.To[0] != "alias@example.com" {
		t.Fatalf("unexpected to addresses: %#v", in.To)
	}
	if in.From != "noreply@tm.openai.com" {
		t.Fatalf("unexpected from: %q", in.From)
	}
	if !strings.Contains(in.Body, "339322") {
		t.Fatalf("expected body to contain OTP, got %q", in.Body)
	}

	updates, err = c.PollUpdates(context.Background())
	if err != nil {
		t.Fatalf("second PollUpdates() error = %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("expected no duplicate updates after UID checkpoint, got %d", len(updates))
	}
}

func TestDecodeMessageBody_QuotedPrintableHTML(t *testing.T) {
	raw := "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n<html><body>Your ChatGPT code is <b>339322</b></body></html>"
	body, err := decodeMessageBody(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("decodeMessageBody() error = %v", err)
	}
	if !strings.Contains(body, "339322") {
		t.Fatalf("expected decoded html body to contain OTP, got %q", body)
	}
}

func TestWrapTransferEncoding_Base64(t *testing.T) {
	r, err := wrapTransferEncoding(strings.NewReader("MzM5MzIy"), "base64")
	if err != nil {
		t.Fatalf("wrapTransferEncoding() error = %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decoded base64: %v", err)
	}
	if string(b) != "339322" {
		t.Fatalf("expected decoded base64 value 339322, got %q", string(b))
	}
}

func TestSmallestUIDWindow(t *testing.T) {
	tests := []struct {
		name  string
		uids  []uint32
		after uint32
		limit int
		want  []uint32
	}{
		{
			name:  "keeps smallest greater than checkpoint",
			uids:  []uint32{9, 7, 12, 10, 8, 20},
			after: 7,
			limit: 3,
			want:  []uint32{8, 9, 10},
		},
		{
			name:  "returns empty when no newer",
			uids:  []uint32{1, 2, 3},
			after: 10,
			limit: 2,
			want:  nil,
		},
		{
			name:  "respects non-positive limit",
			uids:  []uint32{1, 2, 3},
			after: 0,
			limit: 0,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := smallestUIDWindow(tt.uids, tt.after, tt.limit)
			if len(got) != len(tt.want) {
				t.Fatalf("unexpected output length: got=%d want=%d (%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("unexpected output at %d: got=%d want=%d full=%v", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}
