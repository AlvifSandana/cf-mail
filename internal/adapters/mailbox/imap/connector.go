package imap

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"regexp"
	"sort"
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
	UidSearch(criteria *goimap.SearchCriteria) ([]uint32, error)
	UidFetch(seqset *goimap.SeqSet, items []goimap.FetchItem, ch chan *goimap.Message) error
	Logout() error
	Close() error
}

type Deps struct {
	DialTLS func(ctx context.Context, addr string, tlsCfg *tls.Config) (Client, error)
}

type Connector struct {
	client  Client
	mailbox MailboxStatus
	lastUID uint32
	mu      sync.Mutex
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

const defaultPollTimeout = 15 * time.Second

const (
	maxUIDsPerPoll    = 200
	uidFetchChunkSize = 50
	maxMIMEPartDepth  = 8
	maxMIMEParts      = 64
)

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
	_, err := c.PollUpdates(ctx)
	return err
}

func (c *Connector) PollUpdates(ctx context.Context) ([]IncomingEmail, error) {
	if c == nil {
		return nil, fmt.Errorf("imap connector is nil")
	}
	if c.client == nil {
		return nil, fmt.Errorf("imap connector client is nil")
	}
	if strings.TrimSpace(c.mailbox.Name) == "" {
		return nil, fmt.Errorf("imap connector mailbox is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("imap connector is closed")
	}

	pollCtx, cancel := context.WithTimeout(ctx, defaultPollTimeout)
	defer cancel()

	if err := callWithContext(pollCtx, func() error {
		_, err := c.client.Select(c.mailbox.Name, false)
		return err
	}, func() {
		_ = c.client.Close()
	}); err != nil {
		return nil, err
	}

	criteria := goimap.NewSearchCriteria()
	criteria.WithoutFlags = []string{goimap.SeenFlag}
	uidRange := new(goimap.SeqSet)
	uidRange.AddRange(c.lastUID+1, 0)
	criteria.Uid = uidRange

	uids, err := searchUIDsWithContext(pollCtx, func() ([]uint32, error) {
		return c.client.UidSearch(criteria)
	}, func() {
		_ = c.client.Close()
	})
	if err != nil {
		return nil, err
	}
	if len(uids) == 0 {
		return nil, nil
	}

	filtered := smallestUIDWindow(uids, c.lastUID, maxUIDsPerPoll)
	if len(filtered) == 0 {
		return nil, nil
	}

	section := &goimap.BodySectionName{Peek: true, Partial: []int{0, maxBodyBytes * 2}}
	items := []goimap.FetchItem{goimap.FetchUid, goimap.FetchEnvelope, goimap.FetchInternalDate, section.FetchItem()}

	out := make([]IncomingEmail, 0, len(filtered))
	maxCommittedUID := c.lastUID

	for start := 0; start < len(filtered); start += uidFetchChunkSize {
		end := start + uidFetchChunkSize
		if end > len(filtered) {
			end = len(filtered)
		}
		chunk := filtered[start:end]

		seqset := new(goimap.SeqSet)
		seqset.AddNum(chunk...)

		messages, fetchErr := fetchMessagesWithContext(pollCtx, func(ch chan *goimap.Message) error {
			return c.client.UidFetch(seqset, items, ch)
		}, func() {
			_ = c.client.Close()
		}, len(chunk))
		if fetchErr != nil {
			return nil, fetchErr
		}

		chunkMaxUID := maxCommittedUID
		for _, msg := range messages {
			if err := pollCtx.Err(); err != nil {
				return nil, err
			}
			if msg == nil {
				continue
			}
			if msg.Uid > chunkMaxUID {
				chunkMaxUID = msg.Uid
			}
			in, ok := mapFetchedMessage(msg, section)
			if !ok {
				return nil, fmt.Errorf("map fetched imap message uid=%d", msg.Uid)
			}
			normalized, normErr := NormalizeIncomingEmail(in)
			if normErr != nil {
				return nil, fmt.Errorf("normalize fetched imap message uid=%d: %w", msg.Uid, normErr)
			}
			out = append(out, normalized)
		}

		if chunkMaxUID > maxCommittedUID {
			maxCommittedUID = chunkMaxUID
		}
	}

	c.lastUID = maxCommittedUID
	return out, nil
}

func (c *Connector) Close() error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeOnce.Do(func() {
		c.closed = true
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

func searchUIDsWithContext(ctx context.Context, fn func() ([]uint32, error), onCancel func()) ([]uint32, error) {
	type result struct {
		uids []uint32
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		uids, err := fn()
		ch <- result{uids: uids, err: err}
	}()

	select {
	case <-ctx.Done():
		if onCancel != nil {
			onCancel()
		}
		return nil, ctx.Err()
	case r := <-ch:
		return r.uids, r.err
	}
}

func fetchMessagesWithContext(ctx context.Context, fn func(ch chan *goimap.Message) error, onCancel func(), capacity int) ([]*goimap.Message, error) {
	if capacity <= 0 {
		capacity = 1
	}

	msgCh := make(chan *goimap.Message, capacity)
	fetchErrCh := make(chan error, 1)
	go func() {
		fetchErrCh <- fn(msgCh)
	}()

	out := make([]*goimap.Message, 0, capacity)
	activeMsgCh := msgCh
	activeFetchErrCh := fetchErrCh
	for activeMsgCh != nil || activeFetchErrCh != nil {
		select {
		case <-ctx.Done():
			if onCancel != nil {
				onCancel()
			}
			return nil, ctx.Err()
		case err := <-activeFetchErrCh:
			if err != nil {
				return nil, err
			}
			activeFetchErrCh = nil
		case msg, ok := <-activeMsgCh:
			if !ok {
				activeMsgCh = nil
				continue
			}
			out = append(out, msg)
		}
	}

	return out, nil
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

func (c *goIMAPClient) UidSearch(criteria *goimap.SearchCriteria) ([]uint32, error) {
	return c.inner.UidSearch(criteria)
}

func (c *goIMAPClient) UidFetch(seqset *goimap.SeqSet, items []goimap.FetchItem, ch chan *goimap.Message) error {
	return c.inner.UidFetch(seqset, items, ch)
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

func mapFetchedMessage(msg *goimap.Message, section *goimap.BodySectionName) (IncomingEmail, bool) {
	if msg == nil || msg.Envelope == nil {
		return IncomingEmail{}, false
	}

	to := make([]string, 0, len(msg.Envelope.To))
	for _, a := range msg.Envelope.To {
		if a == nil {
			continue
		}
		v := strings.TrimSpace(a.Address())
		if v == "" {
			continue
		}
		to = append(to, v)
	}
	if len(to) == 0 {
		return IncomingEmail{}, false
	}

	from := ""
	if len(msg.Envelope.From) > 0 && msg.Envelope.From[0] != nil {
		from = strings.TrimSpace(msg.Envelope.From[0].Address())
	}

	bodyText := ""
	bodyLiteral := msg.GetBody(section)
	if bodyLiteral == nil {
		bodyLiteral = fallbackBodyLiteral(msg.Body)
	}
	if bodyLiteral != nil {
		if decoded, err := decodeMessageBody(bodyLiteral); err == nil {
			bodyText = decoded
		}
	}

	receivedAt := msg.InternalDate
	if receivedAt.IsZero() {
		receivedAt = msg.Envelope.Date
	}

	return IncomingEmail{
		To:         to,
		From:       from,
		Subject:    strings.TrimSpace(msg.Envelope.Subject),
		MessageID:  strings.TrimSpace(msg.Envelope.MessageId),
		Body:       bodyText,
		Snippet:    deriveSnippet(bodyText, defaultSnippetMax),
		ReceivedAt: receivedAt,
	}, true
}

func fallbackBodyLiteral(body map[*goimap.BodySectionName]goimap.Literal) goimap.Literal {
	if len(body) == 0 {
		return nil
	}

	type candidate struct {
		name string
		lit  goimap.Literal
	}
	candidates := make([]candidate, 0, len(body))
	for section, literal := range body {
		if literal == nil {
			continue
		}
		name := ""
		if section != nil {
			name = string(section.FetchItem())
		}
		candidates = append(candidates, candidate{name: name, lit: literal})
	}
	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].name != candidates[j].name {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].lit.Len() < candidates[j].lit.Len()
	})

	return candidates[0].lit
}

func smallestUIDWindow(uids []uint32, after uint32, limit int) []uint32 {
	if limit <= 0 || len(uids) == 0 {
		return nil
	}

	h := make(maxUIDHeap, 0, limit)
	for _, uid := range uids {
		if uid <= after {
			continue
		}
		if len(h) < limit {
			heap.Push(&h, uid)
			continue
		}
		if uid >= h[0] {
			continue
		}
		h[0] = uid
		heap.Fix(&h, 0)
	}

	out := make([]uint32, len(h))
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(&h).(uint32)
	}
	return out
}

type maxUIDHeap []uint32

func (h maxUIDHeap) Len() int           { return len(h) }
func (h maxUIDHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h maxUIDHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *maxUIDHeap) Push(x any) {
	*h = append(*h, x.(uint32))
}

func (h *maxUIDHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func decodeMessageBody(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBodyBytes*2))
	if err != nil {
		return "", err
	}

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return string(raw), nil
	}

	partsSeen := 0
	body, err := decodeBodyPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0, &partsSeen)
	if err != nil {
		return strings.TrimSpace(string(raw)), nil
	}

	return strings.TrimSpace(body), nil
}

func decodeBodyPart(contentType, transferEncoding string, body io.Reader, depth int, partsSeen *int) (string, error) {
	if depth > maxMIMEPartDepth {
		return "", fmt.Errorf("mime nesting depth exceeded")
	}
	if partsSeen != nil {
		*partsSeen++
		if *partsSeen > maxMIMEParts {
			return "", fmt.Errorf("mime part count exceeded")
		}
	}

	decodedReader, err := wrapTransferEncoding(body, transferEncoding)
	if err != nil {
		return "", err
	}

	mediaType, params, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			b, _ := io.ReadAll(decodedReader)
			return string(b), nil
		}
		mr := multipart.NewReader(decodedReader, boundary)
		plainParts := make([]string, 0, 2)
		htmlParts := make([]string, 0, 2)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", err
			}
			partBody, err := decodeBodyPart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1, partsSeen)
			_ = part.Close()
			if err != nil {
				continue
			}
			partCT, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			partCT = strings.ToLower(strings.TrimSpace(partCT))
			switch partCT {
			case "text/plain":
				plainParts = append(plainParts, partBody)
			case "text/html":
				htmlParts = append(htmlParts, partBody)
			default:
				if strings.TrimSpace(partBody) != "" {
					plainParts = append(plainParts, partBody)
				}
			}
		}
		if len(plainParts) > 0 {
			return strings.Join(plainParts, "\n"), nil
		}
		if len(htmlParts) > 0 {
			return strings.Join(htmlParts, "\n"), nil
		}
		return "", nil
	}

	b, err := io.ReadAll(decodedReader)
	if err != nil {
		return "", err
	}
	text := string(b)
	if mediaType == "text/html" {
		text = stripHTML(text)
	}
	return text, nil
}

func wrapTransferEncoding(r io.Reader, transferEncoding string) (io.Reader, error) {
	enc := strings.ToLower(strings.TrimSpace(transferEncoding))
	switch enc {
	case "", "7bit", "8bit", "binary":
		return r, nil
	case "quoted-printable":
		return quotedprintable.NewReader(r), nil
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, r), nil
	default:
		return r, nil
	}
}

var htmlTagRe = regexp.MustCompile(`(?s)<[^>]+>`)

func stripHTML(v string) string {
	v = htmlTagRe.ReplaceAllString(v, " ")
	v = html.UnescapeString(v)
	v = strings.ReplaceAll(v, "\u00a0", " ")
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	return compactWhitespace(v)
}
