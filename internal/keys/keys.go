// Package keys implements a small vim-style keybinding dispatcher.
//
// The UI layer translates platform key events into Press values (a printable
// rune plus modifier flags) and feeds them to a Binder. The Binder tracks any
// pending multi-key prefix (e.g. "g" for "gg") and returns an Action when a
// sequence resolves.
package keys

import "strings"

type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert        // composing — keys go straight to the editor
	ModeSearch        // ":" / "/" prompt
)

// Action is the resolved high-level command.
type Action string

const (
	ActNone        Action = ""
	ActUp          Action = "up"
	ActDown        Action = "down"
	ActPageUp      Action = "page-up"
	ActPageDown    Action = "page-down"
	ActTop         Action = "top"
	ActBottom      Action = "bottom"
	ActOpen        Action = "open"
	ActBack        Action = "back"
	ActArchive     Action = "archive"
	ActTrash       Action = "trash"
	ActStar        Action = "star"
	ActMarkRead    Action = "mark-read"
	ActMarkUnread  Action = "mark-unread"
	ActCompose     Action = "compose"
	ActReply       Action = "reply"
	ActReplyAll    Action = "reply-all"
	ActForward     Action = "forward"
	ActSearch      Action = "search"
	ActNextMatch   Action = "next-match"
	ActPrevMatch   Action = "prev-match"
	ActRefresh     Action = "refresh"
	ActQuit        Action = "quit"
	ActHelp        Action = "help"
	ActGotoInbox   Action = "goto-inbox"
	ActGotoStarred Action = "goto-starred"
	ActGotoSent    Action = "goto-sent"
	ActGotoTrash   Action = "goto-trash"
	ActSend          Action = "send"
	ActCancel        Action = "cancel"
	ActEnterInsert   Action = "enter-insert"
	ActSwitchAccount Action = "switch-account"
	ActSettings      Action = "settings"
	ActYank          Action = "yank"
)

// Press is a single key event reduced to text + modifiers.
// Name is "" for printable runes (use Rune); otherwise a special key name like
// "Enter", "Escape", "Tab", "Backspace".
type Press struct {
	Rune rune
	Name string
	Ctrl bool
}

// Binder holds the pending prefix state.
type Binder struct {
	pending string
}

// Reset clears any in-progress multi-key sequence (call when leaving a mode).
func (b *Binder) Reset() { b.pending = "" }

// Pending returns the in-progress prefix, useful for status-line display.
func (b *Binder) Pending() string { return b.pending }

// Resolve consumes a key press in the given mode and returns an Action.
// Returning ActNone with a non-empty Pending() means the press was absorbed
// as part of a multi-key sequence.
func (b *Binder) Resolve(mode Mode, p Press) Action {
	if mode == ModeInsert || mode == ModeSearch {
		return ActNone
	}

	// Build a normalized token for the press.
	tok := tokenFor(p)
	if tok == "" {
		return ActNone
	}

	seq := b.pending + tok
	if act, ok := normalBindings[seq]; ok {
		b.pending = ""
		return act
	}
	// Could this be a prefix of a known binding?
	for k := range normalBindings {
		if strings.HasPrefix(k, seq) && k != seq {
			b.pending = seq
			return ActNone
		}
	}
	// Unknown — drop pending and try the press alone.
	b.pending = ""
	if act, ok := normalBindings[tok]; ok {
		return act
	}
	return ActNone
}

func tokenFor(p Press) string {
	switch p.Name {
	case "":
		if p.Rune == 0 {
			return ""
		}
		if p.Ctrl {
			return "<C-" + string(p.Rune) + ">"
		}
		return string(p.Rune)
	case "Enter", "Return":
		return "<CR>"
	case "Escape":
		return "<Esc>"
	case "Tab":
		return "<Tab>"
	case "Backspace":
		return "<BS>"
	case "Space":
		return " "
	case "←":
		return "h"
	case "→":
		return "l"
	case "↑":
		return "k"
	case "↓":
		return "j"
	}
	return ""
}

// normalBindings is the vim-flavored keymap for normal mode.
// Multi-key sequences are written verbatim ("gg", "dd").
var normalBindings = map[string]Action{
	"j":       ActDown,
	"k":       ActUp,
	"<C-d>":   ActPageDown,
	"<C-u>":   ActPageUp,
	"<C-f>":   ActPageDown,
	"<C-b>":   ActPageUp,
	"gg":      ActTop,
	"G":       ActBottom,
	"<CR>":    ActOpen,
	"l":       ActOpen,
	"o":       ActOpen,
	"h":       ActBack,
	"<Esc>":   ActBack,
	"e":       ActArchive,
	"u":       ActMarkUnread,
	"U":       ActMarkRead,
	"dd":      ActTrash,
	"s":       ActStar,
	"c":       ActCompose,
	"r":       ActReply,
	"a":       ActReplyAll,
	"f":       ActForward,
	"/":       ActSearch,
	"n":       ActNextMatch,
	"N":       ActPrevMatch,
	"R":       ActRefresh,
	"<C-r>":   ActRefresh,
	"q":       ActQuit,
	"?":       ActHelp,
	"gi":      ActGotoInbox,
	"gs":      ActGotoStarred,
	"gt":      ActGotoSent,
	"gT":      ActGotoTrash,
	"ga":      ActSwitchAccount,
	",":       ActSettings,
	"y":       ActYank,
	"<C-s>":   ActSend,
	"i":       ActEnterInsert,
}
