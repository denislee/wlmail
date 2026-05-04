package keys

import "testing"

func TestSimpleBindings(t *testing.T) {
	cases := []struct {
		press Press
		want  Action
	}{
		{Press{Rune: 'j'}, ActDown},
		{Press{Rune: 'k'}, ActUp},
		{Press{Rune: 'G'}, ActBottom},
		{Press{Name: "Enter"}, ActOpen},
		{Press{Rune: 'd', Ctrl: true}, ActPageDown},
		{Press{Rune: 's', Ctrl: true}, ActSend},
	}
	for _, c := range cases {
		var b Binder
		got := b.Resolve(ModeNormal, c.press)
		if got != c.want {
			t.Errorf("Resolve(%v) = %q, want %q", c.press, got, c.want)
		}
	}
}

func TestMultiKeySequences(t *testing.T) {
	var b Binder
	if got := b.Resolve(ModeNormal, Press{Rune: 'g'}); got != ActNone {
		t.Fatalf("first g returned %q, want pending none", got)
	}
	if b.Pending() != "g" {
		t.Fatalf("pending = %q, want g", b.Pending())
	}
	if got := b.Resolve(ModeNormal, Press{Rune: 'g'}); got != ActTop {
		t.Errorf("gg = %q, want %q", got, ActTop)
	}
	if b.Pending() != "" {
		t.Errorf("pending after match = %q, want empty", b.Pending())
	}

	// gi
	b.Reset()
	b.Resolve(ModeNormal, Press{Rune: 'g'})
	if got := b.Resolve(ModeNormal, Press{Rune: 'i'}); got != ActGotoInbox {
		t.Errorf("gi = %q, want %q", got, ActGotoInbox)
	}

	// dd
	b.Reset()
	b.Resolve(ModeNormal, Press{Rune: 'd'})
	if got := b.Resolve(ModeNormal, Press{Rune: 'd'}); got != ActTrash {
		t.Errorf("dd = %q, want %q", got, ActTrash)
	}

	// "g" followed by an unknown key should clear pending and ignore.
	b.Reset()
	b.Resolve(ModeNormal, Press{Rune: 'g'})
	if got := b.Resolve(ModeNormal, Press{Rune: 'z'}); got != ActNone {
		t.Errorf("gz = %q, want ActNone", got)
	}
	if b.Pending() != "" {
		t.Errorf("pending after gz = %q, want empty", b.Pending())
	}
}

func TestInsertAndSearchModesPassThrough(t *testing.T) {
	var b Binder
	if got := b.Resolve(ModeInsert, Press{Rune: 'j'}); got != ActNone {
		t.Errorf("insert j = %q, want ActNone", got)
	}
	if got := b.Resolve(ModeSearch, Press{Rune: 'q'}); got != ActNone {
		t.Errorf("search q = %q, want ActNone", got)
	}
}
