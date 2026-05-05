package mail

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

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

// List returns up to max message summaries matching the given Gmail query.
func (c *Client) List(ctx context.Context, query string, max int64) ([]Summary, error) {
	var allMessages []*gmail.Message
	pageToken := ""

	for {
		req := c.svc.Users.Messages.List(c.user).Q(query).Context(ctx)
		rem := max - int64(len(allMessages))
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

		allMessages = append(allMessages, resp.Messages...)
		pageToken = resp.NextPageToken

		if pageToken == "" || int64(len(allMessages)) >= max {
			break
		}
	}

	if int64(len(allMessages)) > max {
		allMessages = allMessages[:max]
	}

	type result struct {
		idx int
		s   Summary
		err error
	}
	resChan := make(chan result, len(allMessages))
	sem := make(chan struct{}, 10) // limit concurrency

	for i, m := range allMessages {
		go func(i int, id string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := c.summary(ctx, id)
			resChan <- result{i, s, err}
		}(i, m.Id)
	}

	resMap := make(map[int]Summary)
	for i := 0; i < len(allMessages); i++ {
		res := <-resChan
		if res.err == nil {
			resMap[res.idx] = res.s
		}
	}

	out := make([]Summary, 0, len(resMap))
	for i := 0; i < len(allMessages); i++ {
		if s, ok := resMap[i]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *Client) summary(ctx context.Context, id string) (Summary, error) {
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
		converter := md.NewConverter("", true, nil)
		markdown, err := converter.ConvertString(htmlContent)
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
	dec := &mime.WordDecoder{}
	out, err := dec.DecodeHeader(v)
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
