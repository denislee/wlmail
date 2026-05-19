package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Shared decoders / converters. mime.WordDecoder is safe for concurrent
// use; html-to-markdown's Converter is not, so guard it with a mutex —
// still cheaper than rebuilding it per message.
var (
	mimeDecoder = &mime.WordDecoder{}

	htmlConverterOnce sync.Once
	htmlConverter     *md.Converter
	htmlConverterMu   sync.Mutex
)

func getHTMLConverter() *md.Converter {
	htmlConverterOnce.Do(func() {
		htmlConverter = md.NewConverter("", true, nil)
	})
	return htmlConverter
}

const (
	LabelInbox   = "INBOX"
	LabelStarred = "STARRED"
	LabelSent    = "SENT"
	LabelTrash   = "TRASH"
	LabelUnread  = "UNREAD"
)

type Client struct {
	svc  *gmail.Service
	user string // "me"
}

type Summary struct {
	ID       string
	ThreadID string
	From     string
	Subject  string
	Snippet  string
	Date     time.Time
	Unread   bool
	Starred  bool
}

type Span struct {
	Text     string
	Bold     bool
	Italic   bool
	URL      string
	ImageURL string
}

type RichBody []Span

type Message struct {
	Summary
	To       string
	Cc       string
	Body     RichBody // enriched text (parsed from Markdown); nil for plain-text mails
	Plain    string   // raw text body (Markdown or plain text)
	Headers  map[string]string
}

func New(ctx context.Context, httpClient *http.Client) (*Client, error) {
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, err
	}
	return &Client{svc: svc, user: "me"}, nil
}

// ListIDs returns up to max message IDs matching the given Gmail query.
func (c *Client) ListIDs(ctx context.Context, query string, max int64) ([]string, error) {
	var allIDs []string
	pageToken := ""

	for {
		req := c.svc.Users.Messages.List(c.user).Q(query).Context(ctx)
		rem := max - int64(len(allIDs))
		if rem > 500 {
			req = req.MaxResults(500)
		} else if rem > 0 {
			req = req.MaxResults(rem)
		}

		if pageToken != "" {
			req = req.PageToken(pageToken)
		}

		resp, err := req.Do()
		if err != nil {
			return nil, err
		}

		for _, m := range resp.Messages {
			allIDs = append(allIDs, m.Id)
		}
		pageToken = resp.NextPageToken

		if pageToken == "" || int64(len(allIDs)) >= max {
			break
		}
	}

	if int64(len(allIDs)) > max {
		allIDs = allIDs[:max]
	}
	return allIDs, nil
}

// GetSummaries fetches the summaries for the given list of message IDs
// concurrently, using a bounded worker pool to limit goroutine count and
// API pressure. Returns the successfully fetched summaries (in input
// order), the IDs that failed after retries, and a fatal error (only
// non-nil on context cancellation or a similar bail-out condition).
// Callers must decide how to surface partial failures — silently dropping
// failed IDs hides messages from the UI, which is rarely what we want.
func (c *Client) GetSummaries(ctx context.Context, ids []string) ([]Summary, []string, error) {
	const workers = 10
	if len(ids) == 0 {
		return nil, nil, nil
	}

	type job struct {
		idx int
		id  string
	}
	type result struct {
		idx int
		s   Summary
		err error
	}

	jobs := make(chan job)
	results := make(chan result, len(ids))

	var wg sync.WaitGroup
	n := workers
	if n > len(ids) {
		n = len(ids)
	}
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				s, err := c.summary(ctx, j.id)
				results <- result{idx: j.idx, s: s, err: err}
			}
		}()
	}
	go func() {
		for i, id := range ids {
			select {
			case <-ctx.Done():
				close(jobs)
				return
			case jobs <- job{idx: i, id: id}:
			}
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	indexed := make([]Summary, len(ids))
	mask := make([]bool, len(ids))
	var ctxErr error
	for r := range results {
		if r.err == nil {
			indexed[r.idx] = r.s
			mask[r.idx] = true
			continue
		}
		if errors.Is(r.err, context.Canceled) || errors.Is(r.err, context.DeadlineExceeded) {
			ctxErr = r.err
		}
	}
	out := make([]Summary, 0, len(ids))
	var failed []string
	for i, ok := range mask {
		if ok {
			out = append(out, indexed[i])
		} else {
			failed = append(failed, ids[i])
		}
	}
	if ctxErr != nil && len(out) == 0 {
		return nil, failed, ctxErr
	}
	return out, failed, nil
}

// HistoryChanges describes the mailbox deltas returned by Gmail's
// users.history endpoint since the previous call.
type HistoryChanges struct {
	Added         []string
	Removed       []string
	LabelsAdded   map[string][]string
	LabelsRemoved map[string][]string
}

// History returns mailbox changes since startHistoryID, paginating
// internally. The second return value is the latest historyId observed
// (suitable for storing as the next baseline).
func (c *Client) History(ctx context.Context, startHistoryID string) (HistoryChanges, string, error) {
	out := HistoryChanges{
		LabelsAdded:   map[string][]string{},
		LabelsRemoved: map[string][]string{},
	}
	startID, err := strconv.ParseUint(startHistoryID, 10, 64)
	if err != nil {
		return out, "", fmt.Errorf("history: invalid baseline %q: %w", startHistoryID, err)
	}
	added := map[string]struct{}{}
	removed := map[string]struct{}{}
	latest := startHistoryID
	pageToken := ""
	for {
		req := c.svc.Users.History.List(c.user).StartHistoryId(startID).Context(ctx)
		if pageToken != "" {
			req = req.PageToken(pageToken)
		}
		resp, err := req.Do()
		if err != nil {
			return out, "", err
		}
		if resp.HistoryId != 0 {
			latest = strconv.FormatUint(resp.HistoryId, 10)
		}
		for _, h := range resp.History {
			for _, m := range h.MessagesAdded {
				if m.Message != nil && m.Message.Id != "" {
					added[m.Message.Id] = struct{}{}
					// Capture folder labels (INBOX/SENT/TRASH/…) carried on
					// the new message itself; summary() drops everything
					// except UNREAD/STARRED, so without this the message
					// would be inserted with no folder label and never
					// appear in listByLabel results.
					if len(m.Message.LabelIds) > 0 {
						out.LabelsAdded[m.Message.Id] = append(out.LabelsAdded[m.Message.Id], m.Message.LabelIds...)
					}
				}
			}
			for _, m := range h.MessagesDeleted {
				if m.Message != nil && m.Message.Id != "" {
					removed[m.Message.Id] = struct{}{}
					delete(added, m.Message.Id)
					delete(out.LabelsAdded, m.Message.Id)
				}
			}
			for _, la := range h.LabelsAdded {
				if la.Message == nil {
					continue
				}
				out.LabelsAdded[la.Message.Id] = append(out.LabelsAdded[la.Message.Id], la.LabelIds...)
			}
			for _, lr := range h.LabelsRemoved {
				if lr.Message == nil {
					continue
				}
				out.LabelsRemoved[lr.Message.Id] = append(out.LabelsRemoved[lr.Message.Id], lr.LabelIds...)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	for id := range added {
		out.Added = append(out.Added, id)
	}
	for id := range removed {
		out.Removed = append(out.Removed, id)
	}
	return out, latest, nil
}

// CurrentHistoryID returns the current mailbox historyId. Useful for
// seeding the incremental-sync baseline on first run.
func (c *Client) CurrentHistoryID(ctx context.Context) (string, error) {
	p, err := c.svc.Users.GetProfile(c.user).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if p.HistoryId == 0 {
		return "", nil
	}
	return strconv.FormatUint(p.HistoryId, 10), nil
}

// IsHistoryExpired reports whether err indicates that the stored
// historyId is too old to use (Gmail returns 404 in that case).
func IsHistoryExpired(err error) bool {
	if err == nil {
		return false
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		return ge.Code == 404
	}
	return false
}

// isRetryable reports whether err is a transient Gmail/transport error
// that's worth retrying. Rate limits (429) and 5xx are the common cases;
// anything else (auth, not-found, bad request) should fail fast.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		return ge.Code == 429 || (ge.Code >= 500 && ge.Code < 600)
	}
	// Treat unclassified errors (network resets, EOF) as transient so a
	// single blip doesn't silently drop a message from the list.
	return true
}

func (c *Client) summary(ctx context.Context, id string) (Summary, error) {
	const maxAttempts = 4
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			base := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(base / 2)))
			select {
			case <-ctx.Done():
				return Summary{}, ctx.Err()
			case <-time.After(base + jitter):
			}
		}
		s, err := c.summaryOnce(ctx, id)
		if err == nil {
			return s, nil
		}
		last = err
		if !isRetryable(err) {
			return Summary{}, err
		}
	}
	return Summary{}, last
}

func (c *Client) summaryOnce(ctx context.Context, id string) (Summary, error) {
	m, err := c.svc.Users.Messages.Get(c.user, id).
		Format("metadata").
		MetadataHeaders("From", "Subject", "Date").
		Context(ctx).Do()
	if err != nil {
		return Summary{}, err
	}
	s := Summary{
		ID:       m.Id,
		ThreadID: m.ThreadId,
		Snippet:  decodeEntities(m.Snippet),
		Date:     time.UnixMilli(m.InternalDate),
	}
	for _, h := range m.Payload.Headers {
		switch h.Name {
		case "From":
			s.From = decodeMime(h.Value)
		case "Subject":
			s.Subject = decodeMime(h.Value)
		}
	}
	for _, l := range m.LabelIds {
		switch l {
		case LabelUnread:
			s.Unread = true
		case LabelStarred:
			s.Starred = true
		}
	}
	return s, nil
}

func (c *Client) Get(ctx context.Context, id string) (*Message, error) {
	m, err := c.svc.Users.Messages.Get(c.user, id).Format("full").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	msg := &Message{
		Summary: Summary{
			ID:       m.Id,
			ThreadID: m.ThreadId,
			Snippet:  decodeEntities(m.Snippet),
			Date:     time.UnixMilli(m.InternalDate),
		},
		Headers: map[string]string{},
	}
	for _, h := range m.Payload.Headers {
		v := decodeMime(h.Value)
		msg.Headers[h.Name] = v
		switch h.Name {
		case "From":
			msg.From = v
		case "To":
			msg.To = v
		case "Cc":
			msg.Cc = v
		case "Subject":
			msg.Subject = v
		}
	}
	for _, l := range m.LabelIds {
		switch l {
		case LabelUnread:
			msg.Unread = true
		case LabelStarred:
			msg.Starred = true
		}
	}
	msg.Body, msg.Plain = extractBody(m.Payload)
	return msg, nil
}

// Text returns the message body as a plain string, regardless of whether it
// originated from a parsed Markdown body or a raw text/plain part.
func (m *Message) Text() string {
	if m.Plain != "" {
		return m.Plain
	}
	return m.Body.String()
}

func (rb RichBody) String() string {
	var b strings.Builder
	for _, s := range rb {
		b.WriteString(s.Text)
	}
	return b.String()
}

func decodeBase64(s string) ([]byte, error) {
	data, err := base64.URLEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func extractBody(p *gmail.MessagePart) (RichBody, string) {
	if p == nil {
		return nil, ""
	}
	// Walk parts: prefer text/plain; fall back to text/html.
	var plain, htmlContent string
	var walk func(parts []*gmail.MessagePart)
	walk = func(parts []*gmail.MessagePart) {
		for _, sub := range parts {
			if sub.Body != nil && sub.Body.Data != "" {
				data, err := decodeBase64(sub.Body.Data)
				if err != nil {
					continue
				}
				if strings.HasPrefix(sub.MimeType, "text/plain") && plain == "" {
					plain = string(data)
				} else if strings.HasPrefix(sub.MimeType, "text/html") && htmlContent == "" {
					htmlContent = string(data)
				}
			}
			if len(sub.Parts) > 0 {
				walk(sub.Parts)
			}
		}
	}
	walk(p.Parts)
	// If the top-level part itself has data, check its type.
	if plain == "" && htmlContent == "" && p.Body != nil && p.Body.Data != "" {
		data, _ := decodeBase64(p.Body.Data)
		if strings.HasPrefix(p.MimeType, "text/plain") {
			plain = string(data)
		} else if strings.HasPrefix(p.MimeType, "text/html") {
			htmlContent = string(data)
		}
	}

	if htmlContent != "" {
		converter := getHTMLConverter()
		htmlConverterMu.Lock()
		markdown, err := converter.ConvertString(htmlContent)
		htmlConverterMu.Unlock()
		if err != nil {
			return RichBody{{Text: htmlContent}}, htmlContent
		}
		return ParseMarkdown(markdown), markdown
	}
	if plain != "" {
		return ParseMarkdown(plain), plain
	}
	return nil, ""
}

func ParseMarkdown(markdown string) RichBody {
	reader := text.NewReader([]byte(markdown))
	doc := goldmark.DefaultParser().Parse(reader)

	var rb RichBody
	var walk func(ast.Node, bool) (ast.WalkStatus, error)
	walk = func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			switch n.Kind() {
			case ast.KindParagraph, ast.KindHeading, ast.KindListItem, ast.KindTextBlock:
				rb = append(rb, Span{Text: "\n"}, Span{Text: "\n"})
			}
			return ast.WalkContinue, nil
		}

		switch n.Kind() {
		case ast.KindText:
			t := n.(*ast.Text)
			content := string(t.Value(reader.Source()))
			parts := strings.Split(content, "\n")

			addSpan := func(text string) {
				span := Span{Text: text}
				for p := n.Parent(); p != nil; p = p.Parent() {
					switch p.Kind() {
					case ast.KindEmphasis:
						em := p.(*ast.Emphasis)
						if em.Level == 2 {
							span.Bold = true
						} else {
							span.Italic = true
						}
					case ast.KindLink:
						l := p.(*ast.Link)
						span.URL = string(l.Destination)
					case ast.KindAutoLink:
						l := p.(*ast.AutoLink)
						span.URL = string(l.URL(reader.Source()))
					}
				}
				rb = append(rb, span)
			}

			for i, part := range parts {
				if i > 0 {
					rb = append(rb, Span{Text: "\n"})
				}
				if part != "" {
					addSpan(part)
				}
			}
			if t.HardLineBreak() || t.SoftLineBreak() {
				rb = append(rb, Span{Text: "\n"})
			}

		case ast.KindHeading:
			h := n.(*ast.Heading)
			rb = append(rb, Span{Text: strings.Repeat("#", h.Level) + " ", Bold: true})
		case ast.KindListItem:
			rb = append(rb, Span{Text: "• "})
		case ast.KindImage:
			img := n.(*ast.Image)
			src := string(img.Destination)
			if !isTrackingPixel(src) {
				rb = append(rb, Span{ImageURL: src, Text: "[IMAGE]"})
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	}

	_ = ast.Walk(doc, walk)

	// Trim leading/trailing newlines only
	for len(rb) > 0 && rb[0].Text == "\n" {
		rb = rb[1:]
	}
	for len(rb) > 0 && rb[len(rb)-1].Text == "\n" {
		rb = rb[:len(rb)-1]
	}
	return rb
}

func isTrackingPixel(url string) bool {
	low := strings.ToLower(url)
	indicators := []string{
		"pixel", "tracking", "open", "track", "pixel.gif", "pixel.png",
		"google-analytics.com/collect", "doubleclick.net",
	}
	for _, ind := range indicators {
		if strings.Contains(low, ind) {
			return true
		}
	}
	// Many tracking pixels are very short URLs or have specific patterns,
	// but we'll stick to a keyword-based heuristic for now.
	return false
}

func decodeMime(v string) string {
	out, err := mimeDecoder.DecodeHeader(v)
	if err != nil {
		return v
	}
	return out
}

func decodeEntities(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return r.Replace(s)
}

func (c *Client) modify(ctx context.Context, id string, add, remove []string) error {
	_, err := c.svc.Users.Messages.Modify(c.user, id, &gmail.ModifyMessageRequest{
		AddLabelIds:    add,
		RemoveLabelIds: remove,
	}).Context(ctx).Do()
	return err
}

func (c *Client) Archive(ctx context.Context, id string) error {
	return c.modify(ctx, id, nil, []string{LabelInbox})
}

func (c *Client) Trash(ctx context.Context, id string) error {
	_, err := c.svc.Users.Messages.Trash(c.user, id).Context(ctx).Do()
	return err
}

func (c *Client) MarkRead(ctx context.Context, id string) error {
	return c.modify(ctx, id, nil, []string{LabelUnread})
}

func (c *Client) MarkUnread(ctx context.Context, id string) error {
	return c.modify(ctx, id, []string{LabelUnread}, nil)
}

func (c *Client) ToggleStar(ctx context.Context, id string, starred bool) error {
	if starred {
		return c.modify(ctx, id, nil, []string{LabelStarred})
	}
	return c.modify(ctx, id, []string{LabelStarred}, nil)
}

func (c *Client) ClearCache(ctx context.Context) error {
	return nil
}

type Outgoing struct {
	To           string
	Cc           string
	Bcc          string
	Subject      string
	Body         string
	InReplyTo    string // Message-ID header
	References   string
	ThreadID     string
}

// Send composes a plain-text RFC 5322 message and sends it.
func (c *Client) Send(ctx context.Context, o Outgoing) error {
	from, err := c.profileEmail(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", o.To)
	if o.Cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", o.Cc)
	}
	if o.Bcc != "" {
		fmt.Fprintf(&b, "Bcc: %s\r\n", o.Bcc)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", o.Subject))
	if o.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", o.InReplyTo)
	}
	if o.References != "" {
		fmt.Fprintf(&b, "References: %s\r\n", o.References)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(o.Body))
	_ = w.Close()

	raw := base64.URLEncoding.EncodeToString([]byte(b.String()))
	msg := &gmail.Message{Raw: raw}
	if o.ThreadID != "" {
		msg.ThreadId = o.ThreadID
	}
	_, err = c.svc.Users.Messages.Send(c.user, msg).Context(ctx).Do()
	return err
}

func (c *Client) profileEmail(ctx context.Context) (string, error) {
	p, err := c.svc.Users.GetProfile(c.user).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return p.EmailAddress, nil
}
