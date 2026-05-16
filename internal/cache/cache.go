// Package cache stores Gmail message metadata + bodies in a per-account
// SQLite database, so the UI renders from local data immediately and the
// Gmail API is only hit for new/missing items.
package cache

import (
	"container/list"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"wlmail/internal/mail"
)

// bodyCacheCap is the number of parsed message bodies kept in memory.
const bodyCacheCap = 128

// Cache is a SQLite-backed wrapper over *mail.Client.
type Cache struct {
	db  *sql.DB
	api *mail.Client

	bodyMu    sync.Mutex
	bodyList  *list.List
	bodyIndex map[string]*list.Element

	refreshMu sync.Mutex
	refreshes map[string]chan struct{}
}

type bodyEntry struct {
	id   string
	body mail.RichBody
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
	return &Cache{
		db:        db,
		api:       api,
		bodyList:  list.New(),
		bodyIndex: make(map[string]*list.Element),
		refreshes: make(map[string]chan struct{}),
	}, nil
}

func (c *Cache) Close() error { return c.db.Close() }

// ---------- body LRU ----------

func (c *Cache) bodyGet(id string) (mail.RichBody, bool) {
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()
	e, ok := c.bodyIndex[id]
	if !ok {
		return nil, false
	}
	c.bodyList.MoveToFront(e)
	return e.Value.(*bodyEntry).body, true
}

func (c *Cache) bodyPut(id string, body mail.RichBody) {
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()
	if e, ok := c.bodyIndex[id]; ok {
		e.Value.(*bodyEntry).body = body
		c.bodyList.MoveToFront(e)
		return
	}
	e := c.bodyList.PushFront(&bodyEntry{id: id, body: body})
	c.bodyIndex[id] = e
	for c.bodyList.Len() > bodyCacheCap {
		back := c.bodyList.Back()
		if back == nil {
			break
		}
		delete(c.bodyIndex, back.Value.(*bodyEntry).id)
		c.bodyList.Remove(back)
	}
}

func (c *Cache) bodyEvict(id string) {
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()
	if e, ok := c.bodyIndex[id]; ok {
		c.bodyList.Remove(e)
		delete(c.bodyIndex, id)
	}
}

func (c *Cache) bodyClear() {
	c.bodyMu.Lock()
	defer c.bodyMu.Unlock()
	c.bodyList.Init()
	c.bodyIndex = make(map[string]*list.Element)
}

// ---------- low-level CRUD ----------

// replaceLabels rewrites message_labels for id within the given tx.
func replaceLabels(ctx context.Context, tx *sql.Tx, id string, labels []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_labels WHERE message_id = ?`, id); err != nil {
		return err
	}
	for _, l := range labels {
		if l == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO message_labels (message_id, label) VALUES (?, ?)`,
			id, l); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) upsertSummary(ctx context.Context, s mail.Summary, labels []string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages
			(id, thread_id, from_addr, subject, snippet, date_unix, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			thread_id  = excluded.thread_id,
			from_addr  = excluded.from_addr,
			subject    = excluded.subject,
			snippet    = excluded.snippet,
			date_unix  = excluded.date_unix,
			fetched_at = excluded.fetched_at
	`,
		s.ID, s.ThreadID, s.From, s.Subject, s.Snippet,
		s.Date.UnixMilli(), time.Now().Unix(),
	)
	if err != nil {
		return err
	}
	if err := replaceLabels(ctx, tx, s.ID, labels); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Cache) upsertFull(ctx context.Context, m *mail.Message, labels []string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	bodyText := m.Plain
	_, err = tx.ExecContext(ctx, `
		INSERT INTO messages
			(id, thread_id, from_addr, to_addr, cc_addr, subject, snippet, body,
			 date_unix, has_full, fetched_at, message_id, references_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			thread_id   = excluded.thread_id,
			from_addr   = excluded.from_addr,
			to_addr     = excluded.to_addr,
			cc_addr     = excluded.cc_addr,
			subject     = excluded.subject,
			snippet     = excluded.snippet,
			body        = excluded.body,
			date_unix   = excluded.date_unix,
			has_full    = 1,
			fetched_at  = excluded.fetched_at,
			message_id  = excluded.message_id,
			references_ = excluded.references_
	`,
		m.ID, m.ThreadID, m.From, m.To, m.Cc, m.Subject, m.Snippet, bodyText,
		m.Date.UnixMilli(), time.Now().Unix(),
		m.Headers["Message-ID"], m.Headers["References"],
	)
	if err != nil {
		return err
	}
	if err := replaceLabels(ctx, tx, m.ID, labels); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.bodyPut(m.ID, m.Body)
	return nil
}

func (c *Cache) deleteMessage(ctx context.Context, id string) error {
	c.bodyEvict(id)
	_, err := c.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id)
	return err
}

func (c *Cache) addMessageLabel(ctx context.Context, id, label string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO message_labels (message_id, label) VALUES (?, ?)`,
		id, label)
	return err
}

func (c *Cache) removeMessageLabel(ctx context.Context, id, label string) error {
	_, err := c.db.ExecContext(ctx,
		`DELETE FROM message_labels WHERE message_id = ? AND label = ?`,
		id, label)
	return err
}

func (c *Cache) updateLabels(ctx context.Context, id string, mut func([]string) []string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id = ?`, id).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	labels, err := txLabels(ctx, tx, id)
	if err != nil {
		return err
	}
	labels = mut(labels)
	if err := replaceLabels(ctx, tx, id, labels); err != nil {
		return err
	}
	return tx.Commit()
}

func txLabels(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT label FROM message_labels WHERE message_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func addLabel(labels []string, l string) []string {
	for _, x := range labels {
		if x == l {
			return labels
		}
	}
	return append(labels, l)
}

func removeLabel(labels []string, l string) []string {
	out := labels[:0]
	for _, x := range labels {
		if x != l {
			out = append(out, x)
		}
	}
	return out
}

func containsLabel(labels []string, l string) bool {
	for _, x := range labels {
		if x == l {
			return true
		}
	}
	return false
}

// ---------- queries ----------

// labelForFolderQuery maps the Gmail-search strings the UI uses to a label
// name we can match against the cached labels. Anything else returns "" —
// the caller should treat that as "must hit the API".
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
		SELECT m.id, m.thread_id, m.from_addr, m.subject, m.snippet, m.date_unix,
		       CASE WHEN lu.message_id IS NULL THEN 0 ELSE 1 END AS unread,
		       CASE WHEN ls.message_id IS NULL THEN 0 ELSE 1 END AS starred
		FROM messages m
		JOIN message_labels l        ON l.message_id  = m.id AND l.label = ?
		LEFT JOIN message_labels lu  ON lu.message_id = m.id AND lu.label = 'UNREAD'
		LEFT JOIN message_labels ls  ON ls.message_id = m.id AND ls.label = 'STARRED'
		ORDER BY unread DESC, m.date_unix DESC
		LIMIT ?`, label, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []mail.Summary
	for rows.Next() {
		var (
			s              mail.Summary
			ms             int64
			unread, starred int
		)
		if err := rows.Scan(&s.ID, &s.ThreadID, &s.From, &s.Subject, &s.Snippet, &ms, &unread, &starred); err != nil {
			return nil, err
		}
		s.Date = time.UnixMilli(ms)
		s.Unread = unread == 1
		s.Starred = starred == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Cache) getSummary(ctx context.Context, id string) (mail.Summary, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT m.thread_id, m.from_addr, m.subject, m.snippet, m.date_unix,
		       CASE WHEN lu.message_id IS NULL THEN 0 ELSE 1 END,
		       CASE WHEN ls.message_id IS NULL THEN 0 ELSE 1 END
		FROM messages m
		LEFT JOIN message_labels lu ON lu.message_id = m.id AND lu.label = 'UNREAD'
		LEFT JOIN message_labels ls ON ls.message_id = m.id AND ls.label = 'STARRED'
		WHERE m.id = ?`, id)
	var s mail.Summary
	var ms int64
	var unread, starred int
	err := row.Scan(&s.ThreadID, &s.From, &s.Subject, &s.Snippet, &ms, &unread, &starred)
	if err != nil {
		return s, err
	}
	s.ID = id
	s.Date = time.UnixMilli(ms)
	s.Unread = unread == 1
	s.Starred = starred == 1
	return s, nil
}

// getSummariesBatch fetches multiple summaries in a single query. The result
// is keyed by message id; missing ids are simply absent from the map.
func (c *Cache) getSummariesBatch(ctx context.Context, ids []string) (map[string]mail.Summary, error) {
	if len(ids) == 0 {
		return map[string]mail.Summary{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `
		SELECT m.id, m.thread_id, m.from_addr, m.subject, m.snippet, m.date_unix,
		       CASE WHEN lu.message_id IS NULL THEN 0 ELSE 1 END,
		       CASE WHEN ls.message_id IS NULL THEN 0 ELSE 1 END
		FROM messages m
		LEFT JOIN message_labels lu ON lu.message_id = m.id AND lu.label = 'UNREAD'
		LEFT JOIN message_labels ls ON ls.message_id = m.id AND ls.label = 'STARRED'
		WHERE m.id IN (` + placeholders + `)`
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]mail.Summary, len(ids))
	for rows.Next() {
		var s mail.Summary
		var ms int64
		var unread, starred int
		if err := rows.Scan(&s.ID, &s.ThreadID, &s.From, &s.Subject, &s.Snippet, &ms, &unread, &starred); err != nil {
			return nil, err
		}
		s.Date = time.UnixMilli(ms)
		s.Unread = unread == 1
		s.Starred = starred == 1
		out[s.ID] = s
	}
	return out, rows.Err()
}

// storedLabelsBatch returns labels for many ids in one query.
func (c *Cache) storedLabelsBatch(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT message_id, label FROM message_labels WHERE message_id IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string, len(ids))
	for rows.Next() {
		var mid, label string
		if err := rows.Scan(&mid, &label); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], label)
	}
	return out, rows.Err()
}

func (c *Cache) getCached(ctx context.Context, id string) (*mail.Message, bool, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT thread_id, from_addr, to_addr, cc_addr, subject, snippet, body,
		       date_unix, has_full, message_id, references_
		FROM messages WHERE id = ?`, id)
	var (
		m       mail.Message
		ms      int64
		bodyJ   string
		hasFull int
	)
	m.Headers = map[string]string{}
	var msgID, refs string
	err := row.Scan(&m.ThreadID, &m.From, &m.To, &m.Cc, &m.Subject, &m.Snippet, &bodyJ,
		&ms, &hasFull, &msgID, &refs)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	m.Plain = bodyJ
	if cached, ok := c.bodyGet(id); ok {
		m.Body = cached
	} else if bodyJ != "" {
		m.Body = mail.ParseMarkdown(bodyJ)
		c.bodyPut(id, m.Body)
	}

	m.ID = id
	m.Date = time.UnixMilli(ms)
	if msgID != "" {
		m.Headers["Message-ID"] = msgID
	}
	if refs != "" {
		m.Headers["References"] = refs
	}
	labels, err := c.storedLabels(ctx, id)
	if err != nil {
		return nil, false, err
	}
	m.Unread = containsLabel(labels, mail.LabelUnread)
	m.Starred = containsLabel(labels, mail.LabelStarred)
	return &m, hasFull == 1, nil
}

// storedLabels returns the persisted labels for id (empty slice if not
// cached) — useful when we want to merge new labels without losing folder
// labels we already know about.
func (c *Cache) storedLabels(ctx context.Context, id string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT label FROM message_labels WHERE message_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
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
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_labels`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM kv`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.bodyClear()
	return nil
}
