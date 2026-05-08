package cache

import (
	"context"
	"slices"

	"wlmail/internal/mail"
)

// List returns up to max summaries for q. Well-known folder queries
// ("in:inbox", "is:starred", ...) read from the local cache when possible;
// arbitrary searches always go to the API.
//
// Even for cached results we kick off an asynchronous refresh so the next
// call sees fresher data.
func (c *Cache) List(ctx context.Context, q string, max int64) ([]mail.Summary, error) {
	label := labelForFolderQuery(q)
	if label == "" {
		// Search / unknown query: hit the API directly, then upsert.
		return c.fetchAndStore(ctx, q, max)
	}
	cached, err := c.listByLabel(ctx, label, int(max))
	if err != nil {
		return nil, err
	}
	// If the cache has fewer items than requested, we need to fetch more
	// synchronously so the UI can actually show them.
	if len(cached) < int(max) {
		return c.fetchAndStore(ctx, q, max)
	}
	// If we have enough in cache, still kick off a background refresh
	// for the first page to keep things fresh.
	go func() {
		// Best-effort background refresh.
		bg, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, _ = c.fetchAndStore(bg, q, max)
	}()
	return cached, nil
}

func (c *Cache) fetchAndStore(ctx context.Context, q string, max int64) ([]mail.Summary, error) {
	ids, err := c.api.ListIDs(ctx, q, max)
	if err != nil {
		return nil, err
	}

	var missingIDs []string
	cachedMap := make(map[string]mail.Summary)

	for _, id := range ids {
		s, err := c.getSummary(ctx, id)
		if err == nil && s.ID != "" {
			cachedMap[id] = s
		} else {
			missingIDs = append(missingIDs, id)
		}
	}

	var newItems []mail.Summary
	if len(missingIDs) > 0 {
		newItems, err = c.api.GetSummaries(ctx, missingIDs)
		if err != nil {
			return nil, err
		}

		folderLabel := labelForFolderQuery(q)
		for _, s := range newItems {
			labels := []string{}
			if folderLabel != "" {
				labels = append(labels, folderLabel)
			}
			if s.Unread {
				labels = append(labels, mail.LabelUnread)
			}
			if s.Starred {
				labels = append(labels, mail.LabelStarred)
			}
			_ = c.upsertSummary(ctx, s, mergeWithStored(c, ctx, s.ID, labels))
			cachedMap[s.ID] = s
		}
	}

	var items []mail.Summary
	for _, id := range ids {
		if s, ok := cachedMap[id]; ok {
			items = append(items, s)
		}
	}

	slices.SortFunc(items, func(a, b mail.Summary) int {
		if a.Unread != b.Unread {
			if a.Unread {
				return -1
			}
			return 1
		}
		if a.Date.After(b.Date) {
			return -1
		}
		if a.Date.Before(b.Date) {
			return 1
		}
		return 0
	})

	return items, nil
}

// mergeWithStored unions newLabels with whatever was already persisted,
// preserving folder labels (INBOX/SENT/...) that List() wouldn't otherwise
// give us, but updating state labels (UNREAD/STARRED).
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
	// We don't have the raw label list here — best effort. Folder labels
	// can be resynced by a future Sync() implementation.
	return labels
}

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
