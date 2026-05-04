package cache

import (
	"context"
	"slices"
	"testing"
	"time"

	"wlmail/internal/mail"
)

func TestUpsertAndListByLabel(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx := context.Background()
	now := time.UnixMilli(time.Now().UnixMilli())

	rows := []struct {
		s      mail.Summary
		labels []string
	}{
		{mail.Summary{ID: "1", ThreadID: "t1", From: "a@x", Subject: "hi", Date: now},
			[]string{mail.LabelInbox}}, // Newest, but READ
		{mail.Summary{ID: "2", ThreadID: "t2", From: "b@x", Subject: "yo", Date: now.Add(-time.Minute)},
			[]string{mail.LabelInbox, mail.LabelUnread}}, // Older, but UNREAD
		{mail.Summary{ID: "3", ThreadID: "t3", From: "c@x", Subject: "bye", Date: now.Add(-time.Hour)},
			[]string{mail.LabelSent}},
	}
	for _, r := range rows {
		if err := c.upsertSummary(ctx, r.s, r.labels); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	got, err := c.listByLabel(ctx, mail.LabelInbox, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("inbox count = %d, want 2", len(got))
	}
	// ID "2" should be first because it's UNREAD, even though "1" is newer.
	if got[0].ID != "2" {
		t.Errorf("expected unread first; got %q", got[0].ID)
	}
	if got[1].ID != "1" {
		t.Errorf("expected newer read second; got %q", got[1].ID)
	}
}

func TestUpdateLabelsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx := context.Background()
	s := mail.Summary{ID: "x", ThreadID: "t", From: "a@x", Subject: "s", Date: time.Now()}
	if err := c.upsertSummary(ctx, s, []string{mail.LabelInbox, mail.LabelUnread}); err != nil {
		t.Fatal(err)
	}

	if err := c.updateLabels(ctx, "x", func(ls []string) []string {
		return removeLabel(ls, mail.LabelUnread)
	}); err != nil {
		t.Fatal(err)
	}

	labels, err := c.storedLabels(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(labels, mail.LabelUnread) {
		t.Errorf("UNREAD should be gone, got %v", labels)
	}
	if !slices.Contains(labels, mail.LabelInbox) {
		t.Errorf("INBOX should remain, got %v", labels)
	}
}

func TestKVRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ctx := context.Background()
	if v, _ := c.kvGet(ctx, "history"); v != "" {
		t.Errorf("missing key should return empty, got %q", v)
	}
	if err := c.kvSet(ctx, "history", "12345"); err != nil {
		t.Fatal(err)
	}
	v, err := c.kvGet(ctx, "history")
	if err != nil {
		t.Fatal(err)
	}
	if v != "12345" {
		t.Errorf("kv: got %q, want %q", v, "12345")
	}
}
