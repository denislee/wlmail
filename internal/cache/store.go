package cache

import (
	"context"
	"sort"
	"strconv"

	"wlmail/internal/mail"
)

// List returns up to max summaries for q. Well-known folder queries
// ("in:inbox", "is:starred", ...) read from the local cache when possible;
// arbitrary searches always go to the API.
//
// Even for cached results we kick off an asynchronous refresh so the next
// call sees fresher data. Refreshes are coalesced per-query.
func (c *Cache) List(ctx context.Context, q string, max int64) ([]mail.Summary, error) {
	label := labelForFolderQuery(q)
	if label == "" {
		return c.fetchAndStore(ctx, q, max)
	}
	cached, err := c.listByLabel(ctx, label, int(max))
	if err != nil {
		return nil, err
	}
	if len(cached) < int(max) {
		return c.fetchAndStore(ctx, q, max)
	}
	c.backgroundRefresh(q, max)
	return cached, nil
}

// backgroundRefresh kicks off (or joins) a coalesced background refresh
// for q. Only one runs per query key at a time; concurrent calls share
// the in-flight one and return immediately.
func (c *Cache) backgroundRefresh(q string, max int64) {
	key := q + "\x00" + strconv.FormatInt(max, 10)
	c.refreshMu.Lock()
	if _, busy := c.refreshes[key]; busy {
		c.refreshMu.Unlock()
		return
	}
	done := make(chan struct{})
	c.refreshes[key] = done
	c.refreshMu.Unlock()

	go func() {
		defer func() {
			c.refreshMu.Lock()
			delete(c.refreshes, key)
			c.refreshMu.Unlock()
			close(done)
		}()
		bg, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Prefer incremental sync; fall back to a full fetch when history
		// isn't available (cold start or expired baseline).
		if err := c.Sync(bg); err == nil {
			return
		}
		_, _ = c.fetchAndStore(bg, q, max)
	}()
}

func (c *Cache) fetchAndStore(ctx context.Context, q string, max int64) ([]mail.Summary, error) {
	ids, err := c.api.ListIDs(ctx, q, max)
	if err != nil {
		return nil, err
	}

	cachedMap, err := c.getSummariesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}

	var missingIDs []string
	for _, id := range ids {
		if _, ok := cachedMap[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}

	if len(missingIDs) > 0 {
		newItems, err := c.api.GetSummaries(ctx, missingIDs)
		if err != nil {
			return nil, err
		}
		stored, err := c.storedLabelsBatch(ctx, missingIDs)
		if err != nil {
			return nil, err
		}
		folderLabel := labelForFolderQuery(q)
		for _, s := range newItems {
			labels := mergeLabelsFromExisting(stored[s.ID], folderLabel, s.Unread, s.Starred)
			_ = c.upsertSummary(ctx, s, labels)
			cachedMap[s.ID] = s
		}
	}

	items := make([]mail.Summary, 0, len(ids))
	for _, id := range ids {
		if s, ok := cachedMap[id]; ok {
			items = append(items, s)
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Unread != items[j].Unread {
			return items[i].Unread
		}
		return items[i].Date.After(items[j].Date)
	})

	// Best-effort: seed historyId so future syncs can run incrementally.
	if cur, _ := c.kvGet(ctx, "history_id"); cur == "" {
		if hid, err := c.api.CurrentHistoryID(ctx); err == nil && hid != "" {
			_ = c.kvSet(ctx, "history_id", hid)
		}
	}

	return items, nil
}

// mergeLabelsFromExisting unions newly-derived labels with the folder/state
// labels we already persisted. Existing folder labels are preserved (the
// listing endpoint doesn't return them), but UNREAD/STARRED is taken from
// the fresh server state.
func mergeLabelsFromExisting(existing []string, folderLabel string, unread, starred bool) []string {
	var merged []string
	for _, l := range existing {
		if l != mail.LabelUnread && l != mail.LabelStarred {
			merged = append(merged, l)
		}
	}
	if folderLabel != "" {
		merged = addLabel(merged, folderLabel)
	}
	if unread {
		merged = addLabel(merged, mail.LabelUnread)
	}
	if starred {
		merged = addLabel(merged, mail.LabelStarred)
	}
	return merged
}

// mergeWithStored looks up existing labels in one query and merges them
// with newLabels, preserving folder labels (INBOX/SENT/...) while taking
// the new state labels (UNREAD/STARRED).
func mergeWithStored(c *Cache, ctx context.Context, id string, newLabels []string) []string {
	existing, _ := c.storedLabels(ctx, id)
	var merged []string
	for _, l := range existing {
		if l != mail.LabelUnread && l != mail.LabelStarred {
			merged = append(merged, l)
		}
	}
	for _, l := range newLabels {
		merged = addLabel(merged, l)
	}
	return merged
}

// Get reads a message from cache. If the body isn't cached yet, fetches
// from the API and stores it.
func (c *Cache) Get(ctx context.Context, id string) (*mail.Message, error) {
	m, full, err := c.getCached(ctx, id)
	if err != nil {
		return nil, err
	}
	if m != nil && full {
		return m, nil
	}
	fresh, err := c.api.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	labels := derivedLabels(fresh)
	_ = c.upsertFull(ctx, fresh, mergeWithStored(c, ctx, fresh.ID, labels))
	return fresh, nil
}

func derivedLabels(m *mail.Message) []string {
	var labels []string
	if m.Unread {
		labels = append(labels, mail.LabelUnread)
	}
	if m.Starred {
		labels = append(labels, mail.LabelStarred)
	}
	return labels
}

// Sync applies any pending mailbox changes since the last stored historyId.
// Returns an error if no baseline is set (the caller should fall back to a
// full fetchAndStore in that case).
func (c *Cache) Sync(ctx context.Context) error {
	startID, err := c.kvGet(ctx, "history_id")
	if err != nil {
		return err
	}
	if startID == "" {
		return errNoBaseline
	}
	changes, latest, err := c.api.History(ctx, startID)
	if err != nil {
		if mail.IsHistoryExpired(err) {
			_ = c.kvSet(ctx, "history_id", "")
		}
		return err
	}
	for _, id := range changes.Removed {
		_ = c.deleteMessage(ctx, id)
	}
	if len(changes.Added) > 0 {
		sums, err := c.api.GetSummaries(ctx, changes.Added)
		if err == nil {
			stored, _ := c.storedLabelsBatch(ctx, changes.Added)
			for _, s := range sums {
				labels := mergeLabelsFromExisting(stored[s.ID], "", s.Unread, s.Starred)
				_ = c.upsertSummary(ctx, s, labels)
			}
		}
	}
	for id, labels := range changes.LabelsAdded {
		for _, l := range labels {
			_ = c.addMessageLabel(ctx, id, l)
		}
	}
	for id, labels := range changes.LabelsRemoved {
		for _, l := range labels {
			_ = c.removeMessageLabel(ctx, id, l)
		}
	}
	if latest != "" {
		_ = c.kvSet(ctx, "history_id", latest)
	}
	return nil
}

var errNoBaseline = baselineMissing{}

type baselineMissing struct{}

func (baselineMissing) Error() string { return "cache: no history baseline" }

// ---------- write-through modifications ----------

func (c *Cache) Archive(ctx context.Context, id string) error {
	if err := c.api.Archive(ctx, id); err != nil {
		return err
	}
	return c.updateLabels(ctx, id, func(ls []string) []string {
		return removeLabel(ls, mail.LabelInbox)
	})
}

func (c *Cache) Trash(ctx context.Context, id string) error {
	if err := c.api.Trash(ctx, id); err != nil {
		return err
	}
	return c.updateLabels(ctx, id, func(ls []string) []string {
		ls = removeLabel(ls, mail.LabelInbox)
		return addLabel(ls, mail.LabelTrash)
	})
}

func (c *Cache) MarkRead(ctx context.Context, id string) error {
	if err := c.api.MarkRead(ctx, id); err != nil {
		return err
	}
	return c.updateLabels(ctx, id, func(ls []string) []string {
		return removeLabel(ls, mail.LabelUnread)
	})
}

func (c *Cache) MarkUnread(ctx context.Context, id string) error {
	if err := c.api.MarkUnread(ctx, id); err != nil {
		return err
	}
	return c.updateLabels(ctx, id, func(ls []string) []string {
		return addLabel(ls, mail.LabelUnread)
	})
}

func (c *Cache) ToggleStar(ctx context.Context, id string, currentlyStarred bool) error {
	if err := c.api.ToggleStar(ctx, id, currentlyStarred); err != nil {
		return err
	}
	return c.updateLabels(ctx, id, func(ls []string) []string {
		if currentlyStarred {
			return removeLabel(ls, mail.LabelStarred)
		}
		return addLabel(ls, mail.LabelStarred)
	})
}

func (c *Cache) Send(ctx context.Context, o mail.Outgoing) error {
	return c.api.Send(ctx, o)
}
