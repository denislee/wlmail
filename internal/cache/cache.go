// Package cache stores Gmail message metadata + bodies in a per-account
// SQLite database, so the UI renders from local data immediately and the
// Gmail API is only hit for new/missing items.
package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"wlmail/internal/mail"
)

// Cache is a SQLite-backed wrapper over *mail.Client.
type Cache struct {
	db  *sql.DB
	api *mail.Client
}

// Open initializes a cache rooted at dir/cache.db. The given API client is
// used as the source of truth for any data not yet stored locally.
func Open(dir string, api *mail.Client) (*Cache, error) {
	dsn := "file:" + filepath.Join(dir, "cache.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Cache{db: db, api: api}, nil
}

func (c *Cache) Close() error { return c.db.Close() }

// ---------- low-level CRUD ----------

func (c *Cache) upsertSummary(ctx context.Context, s mail.Summary, labels []string) error {
	labelsJSON, _ := json.Marshal(labels)
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO messages
			(id, thread_id, from_addr, subject, snippet, date_unix, labels, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			thread_id  = excluded.thread_id,
			from_addr  = excluded.from_addr,
			subject    = excluded.subject,
			snippet    = excluded.snippet,
			date_unix  = excluded.date_unix,
			labels     = excluded.labels,
			fetched_at = excluded.fetched_at
	`,
		s.ID, s.ThreadID, s.From, s.Subject, s.Snippet,
		s.Date.UnixMilli(), string(labelsJSON), time.Now().Unix(),
	)
	return err
}

func (c *Cache) upsertFull(ctx context.Context, m *mail.Message, labels []string) error {
	labelsJSON, _ := json.Marshal(labels)
	var bodyText string
	if m.Plain != "" {
		bodyText = m.Plain
	} else {
		bj, _ := json.Marshal(m.Body)
		bodyText = string(bj)
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO messages
			(id, thread_id, from_addr, to_addr, cc_addr, subject, snippet, body,
			 date_unix, labels, has_full, fetched_at, message_id, references_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			thread_id   = excluded.thread_id,
			from_addr   = excluded.from_addr,
			to_addr     = excluded.to_addr,
			cc_addr     = excluded.cc_addr,
			subject     = excluded.subject,
			snippet     = excluded.snippet,
			body        = excluded.body,
			date_unix   = excluded.date_unix,
			labels      = excluded.labels,
			has_full    = 1,
			fetched_at  = excluded.fetched_at,
			message_id  = excluded.message_id,
			references_ = excluded.references_
	`,
		m.ID, m.ThreadID, m.From, m.To, m.Cc, m.Subject, m.Snippet, bodyText,
		m.Date.UnixMilli(), string(labelsJSON), time.Now().Unix(),
		m.Headers["Message-ID"], m.Headers["References"],
	)
	return err
}

func (c *Cache) deleteMessage(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
	return err
}

func (c *Cache) updateLabels(ctx context.Context, id string, mut func([]string) []string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT labels FROM messages WHERE id = ?`, id).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil // nothing cached, server is the truth
		}
		return err
	}
	var labels []string
	_ = json.Unmarshal([]byte(raw), &labels)
	labels = mut(labels)
	out, _ := json.Marshal(labels)
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET labels = ? WHERE id = ?`, string(out), id); err != nil {
		return err
	}
	return tx.Commit()
}

func addLabel(labels []string, l string) []string {
	if slices.Contains(labels, l) {
		return labels
	}
	return append(labels, l)
}

func removeLabel(labels []string, l string) []string {
	return slices.DeleteFunc(labels, func(s string) bool { return s == l })
}

// ---------- queries ----------

// labelForFolderQuery maps the Gmail-search strings the UI uses to a label
// name we can match against the cached `labels` JSON. Anything else returns
// "" — the caller should treat that as "must hit the API".
func labelForFolderQuery(q string) string {
	switch strings.TrimSpace(q) {
	case "in:inbox":
		return mail.LabelInbox
	case "is:starred":
		return mail.LabelStarred
	case "in:sent":
		return mail.LabelSent
	case "in:trash":
		return mail.LabelTrash
	}
	return ""
}

func (c *Cache) listByLabel(ctx context.Context, label string, max int) ([]mail.Summary, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, thread_id, from_addr, subject, snippet, date_unix, labels
		FROM messages
		WHERE EXISTS (SELECT 1 FROM json_each(labels) WHERE value = ?)
		ORDER BY (EXISTS (SELECT 1 FROM json_each(labels) WHERE value = 'UNREAD')) DESC, date_unix DESC
		LIMIT ?`, label, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []mail.Summary
	for rows.Next() {
		var (
			s    mail.Summary
			ms   int64
			lblJ string
		)
		if err := rows.Scan(&s.ID, &s.ThreadID, &s.From, &s.Subject, &s.Snippet, &ms, &lblJ); err != nil {
			return nil, err
		}
		s.Date = time.UnixMilli(ms)
		var labels []string
		_ = json.Unmarshal([]byte(lblJ), &labels)
		s.Unread = slices.Contains(labels, mail.LabelUnread)
		s.Starred = slices.Contains(labels, mail.LabelStarred)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Cache) getCached(ctx context.Context, id string) (*mail.Message, bool, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT thread_id, from_addr, to_addr, cc_addr, subject, snippet, body,
		       date_unix, labels, has_full, message_id, references_
		FROM messages WHERE id = ?`, id)
	var (
		m       mail.Message
		ms      int64
		lblJ    string
		bodyJ   string
		hasFull int
	)
	m.Headers = map[string]string{}
	var msgID, refs string
	err := row.Scan(&m.ThreadID, &m.From, &m.To, &m.Cc, &m.Subject, &m.Snippet, &bodyJ,
		&ms, &lblJ, &hasFull, &msgID, &refs)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	if strings.HasPrefix(bodyJ, "[") {
		if err := json.Unmarshal([]byte(bodyJ), &m.Body); err != nil {
			m.Plain = bodyJ
			m.Body = mail.ParseMarkdown(bodyJ)
		}
	} else {
		m.Plain = bodyJ
		m.Body = mail.ParseMarkdown(bodyJ)
	}

	m.ID = id
	m.Date = time.UnixMilli(ms)
	if msgID != "" {
		m.Headers["Message-ID"] = msgID
	}
	if refs != "" {
		m.Headers["References"] = refs
	}
	var labels []string
	_ = json.Unmarshal([]byte(lblJ), &labels)
	m.Unread = slices.Contains(labels, mail.LabelUnread)
	m.Starred = slices.Contains(labels, mail.LabelStarred)
	return &m, hasFull == 1, nil
}

// storedLabels returns the persisted labels JSON for id (empty slice if not
// cached) — useful when we want to merge new labels without losing folder
// labels we already know about.
func (c *Cache) storedLabels(ctx context.Context, id string) ([]string, error) {
	var raw string
	err := c.db.QueryRowContext(ctx, `SELECT labels FROM messages WHERE id = ?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var labels []string
	_ = json.Unmarshal([]byte(raw), &labels)
	return labels, nil
}

// ---------- kv (sync state) ----------

func (c *Cache) kvGet(ctx context.Context, k string) (string, error) {
	var v string
	err := c.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k = ?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (c *Cache) kvSet(ctx context.Context, k, v string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO kv(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		k, v)
	return err
}

func (c *Cache) ClearCache(ctx context.Context) error {
	_, err := c.db.ExecContext(ctx, `DELETE FROM messages; DELETE FROM kv;`)
	return err
}
