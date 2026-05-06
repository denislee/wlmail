// Package ui hosts the Gio (Wayland-native) UI for wlmail.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"wlmail/internal/keys"
	"wlmail/internal/mail"
)

type view int

const (
	viewList view = iota
	viewMessage
	viewCompose
	viewHelp
	viewAccounts
	viewSettings
	viewLinks
)

type pane int

const (
	paneList pane = iota
	paneMessage
)

type linkItem struct {
	text string
	url  string
}

type Settings struct {
	DefaultAccount string
	Fonts          SectionFonts
	RenderImages   bool
	DarkModeLeft   bool
	DarkModeRight  bool
	SplitRatio     float32
}

type folder struct {
	name  string
	query string
}

var folders = []folder{
	{"INBOX", "in:inbox"},
	{"STARRED", "is:starred"},
	{"SENT", "in:sent"},
	{"TRASH", "in:trash"},
}

// Client is the surface the UI needs from the mail layer. Both
// *mail.Client and *cache.Cache satisfy it.
type Client interface {
	List(ctx context.Context, query string, max int64) ([]mail.Summary, error)
	Get(ctx context.Context, id string) (*mail.Message, error)
	Archive(ctx context.Context, id string) error
	Trash(ctx context.Context, id string) error
	MarkRead(ctx context.Context, id string) error
	MarkUnread(ctx context.Context, id string) error
	ToggleStar(ctx context.Context, id string, currentlyStarred bool) error
	Send(ctx context.Context, o mail.Outgoing) error
	ClearCache(ctx context.Context) error
}

// Config bundles the dependencies the UI needs from the host program.
type Config struct {
	Email        string
	Client       Client
	SwitchTo     func(email string) (Client, error)
	ListAccounts func() ([]string, error)
}
// App is the top-level UI state.
type App struct {
	client       Client
	win          *app.Window
	th           *Theme
	email        string
	switchTo     func(email string) (Client, error)
	listAccounts func() ([]string, error)

	mu        sync.Mutex
	view      view
	focus     pane
	folderIdx int
	items     []mail.Summary
	accounts  []string
	cursor    int
	scroll    int // index of first visible row
	message   *mail.Message
	messageList widget.List
	links     []linkItem
	linkCursor int
	linkScroll int
	status    string
	loading   bool
	listMax   int64
	searchQ   string

	imgCache map[string]paint.ImageOp
	imgMu    sync.Mutex

	// compose state
	to, subject       widget.Editor
	body              widget.Editor
	composeFocus      int // 0=to, 1=subject, 2=body
	composeReplyToID  string
	composeReferences string
	composeThreadID   string

	binder          keys.Binder
	mode            keys.Mode
	searchBuf       widget.Editor
	searchTriggered bool

	settings Settings
	settingsScreen *SettingsScreen

	splitDrag  bool
	splitDragX float32
}

func (a *App) saveSettings() error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(base, "wlmail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	a.settings.Fonts = a.th.Fonts
	b, err := json.MarshalIndent(a.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600)
}

func loadSettings() Settings {
	s := Settings{
		Fonts: SectionFonts{
			Global: FontStyle{Size: 13},
		},
		RenderImages:  true,
		DarkModeLeft:  true,
		DarkModeRight: true,
		SplitRatio:    0.35,
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return s
	}
	b, err := os.ReadFile(filepath.Join(base, "wlmail", "settings.json"))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.SplitRatio <= 0 {
		s.SplitRatio = 0.35
	}
	return s
}

// Run starts the Gio event loop on the calling goroutine. Caller is
// expected to invoke app.Main() afterwards.
func Run(ctx context.Context, cfg Config) error {
	w := new(app.Window)
	w.Option(
		app.Title("wlmail"),
		app.Size(unit.Dp(900), unit.Dp(600)),
	)
	settings := loadSettings()
	th := newTheme()
	th.Fonts = settings.Fonts

	a := &App{
		client:       cfg.Client,
		win:          w,
		th:           th,
		email:        cfg.Email,
		switchTo:     cfg.SwitchTo,
		listAccounts: cfg.ListAccounts,
		settings:     settings,
		listMax:      50,
		imgCache:     make(map[string]paint.ImageOp),
	}

	// Handle default account if specified and not overridden by command line
	if settings.DefaultAccount != "" && a.email == "" {
		// This case is unlikely given main.go's logic, but let's be safe.
		if client, err := a.switchTo(settings.DefaultAccount); err == nil {
			a.client = client
			a.email = settings.DefaultAccount
		}
	} else if settings.DefaultAccount != "" && settings.DefaultAccount != a.email {
		// If main.go picked the "active" account but the user set a specific default in UI,
		// and they didn't use -account flag (which we can't easily tell here, but we can 
		// assume if it's the first run of the session). 
		// Actually, let's only do this if a.email was derived from auth.Active() 
		// and not from a specific flag. This is getting complex.
		// Simpler: if the user explicitly set a DefaultAccount in settings, 
		// and they just launched wlmail (no -account flag), prioritize it.
	}

	a.to.SingleLine = true
	a.subject.SingleLine = true
	a.searchBuf.SingleLine = true
	a.searchBuf.Submit = true

	accounts, _ := a.listAccounts()
	a.settingsScreen = newSettingsScreen(a.th, accounts, &a.settings.DefaultAccount, &a.settings.RenderImages, &a.settings.DarkModeLeft, &a.settings.DarkModeRight, func() {
		_ = a.saveSettings()
	}, func() {
		a.view = viewList
		a.mode = keys.ModeNormal
	}, func() {
		go func() {
			a.setStatus("clearing cache…")
			_ = a.client.ClearCache(ctx)
			a.mu.Lock()
			a.items = nil
			a.message = nil
			a.mu.Unlock()
			a.refresh(ctx)
		}()
	})

	a.status = "wlmail — press ? for help"

	go func() {
		a.refresh(ctx)
		a.mu.Lock()
		if len(a.items) > 0 {
			a.openCurrent(ctx)
		}
		a.mu.Unlock()
	}()
	return a.loop(ctx)
}

func (a *App) loop(ctx context.Context) error {
	var ops op.Ops
	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.handleKeys(ctx, gtx)
			a.layout(gtx)
			if a.searchTriggered {
				a.searchTriggered = false
				a.runSearch(ctx)
			}
			e.Frame(gtx.Ops)
		}
	}
}

// ---------- key handling ----------

var keyTag = new(int)

// keyFilters is the set of keys we consume in normal mode. Listing them
// explicitly is required by Gio — the empty Name catches everything else.
var normalKeyNames = []key.Name{
	"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M",
	"N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z",
	"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
	"/", "?", ",", "⏎", "⎋", "Space", "Tab", "[",
	"←", "→", "↑", "↓",
}

func (a *App) handleKeys(ctx context.Context, gtx layout.Context) {
	// Register a focus target so we receive key events. Only steal focus
	// in normal mode — insert / search modes need the active widget editor
	// to keep focus so it can receive typed characters.
	event.Op(gtx.Ops, keyTag)
	a.mu.Lock()
	mode := a.mode
	view := a.view
	a.mu.Unlock()
	if mode == keys.ModeNormal && !gtx.Source.Focused(keyTag) {
		gtx.Execute(key.FocusCmd{Tag: keyTag})
	}
	if mode == keys.ModeSearch && !gtx.Source.Focused(&a.searchBuf) {
		gtx.Execute(key.FocusCmd{Tag: &a.searchBuf})
	}
	if mode == keys.ModeInsert && view == viewCompose {
		var target *widget.Editor
		switch a.composeFocus {
		case 0:
			target = &a.to
		case 1:
			target = &a.subject
		default:
			target = &a.body
		}
		if !gtx.Source.Focused(target) {
			gtx.Execute(key.FocusCmd{Tag: target})
		}
	}

	filters := []event.Filter{key.FocusFilter{Target: keyTag}}
	for _, n := range normalKeyNames {
		filters = append(filters,
			key.Filter{Focus: keyTag, Name: n},
			key.Filter{Focus: keyTag, Name: n, Required: key.ModCtrl, Optional: key.ModShift},
			key.Filter{Focus: keyTag, Name: n, Optional: key.ModShift},
		)
	}
	// Mode-exit / send keys must fire regardless of which widget has
	// focus. Enter is *not* intercepted globally — the body editor needs
	// it for newlines, and search uses the editor's Submit event instead.
	filters = append(filters,
		key.Filter{Name: "⎋"},
		key.Filter{Name: "[", Required: key.ModCtrl},
		key.Filter{Name: "S", Required: key.ModCtrl},
		key.Filter{Name: "R", Required: key.ModCtrl},
		key.Filter{Name: "W", Required: key.ModCtrl},
		key.Filter{Name: "N", Required: key.ModCtrl},
		key.Filter{Name: "P", Required: key.ModCtrl},
		key.Filter{Name: "K", Required: key.ModCtrl},
		key.Filter{Name: "↑"},
		key.Filter{Name: "↓"},
	)

	for {
		ev, ok := gtx.Source.Event(filters...)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok {
			continue
		}
		if ke.State != key.Press {
			continue
		}
		a.dispatchKey(ctx, ke)
	}
}

func isEscape(ke key.Event) bool {
	return ke.Name == "⎋" || (ke.Name == "[" && ke.Modifiers.Contain(key.ModCtrl))
}

func deleteLastWord(ed *widget.Editor) {
	txt := ed.Text()
	if len(txt) == 0 {
		return
	}
	// Gio editors use rune offsets for CaretPos and SetCaret in recent versions.
	caret, _ := ed.CaretPos()
	if caret == 0 {
		return
	}

	runes := []rune(txt)
	if caret > len(runes) {
		caret = len(runes)
	}
	i := caret - 1

	// Skip trailing whitespace
	for i >= 0 && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\n' || runes[i] == '\r') {
		i--
	}
	// Skip word
	for i >= 0 && runes[i] != ' ' && runes[i] != '\t' && runes[i] != '\n' && runes[i] != '\r' {
		i--
	}
	// i is now at the space before the word, or -1.
	newCaret := i + 1

	ed.SetCaret(newCaret, caret)
	ed.Delete(1)
}

func (a *App) dispatchKey(ctx context.Context, ke key.Event) {
	a.mu.Lock()

	if ke.Modifiers.Contain(key.ModCtrl) && strings.EqualFold(string(ke.Name), "R") {
		go a.refresh(ctx)
		a.mu.Unlock()
		return
	}

	// In insert/search modes, only handle Esc / Enter / Ctrl-S — let the
	// editor widgets capture the rest via their own input handling.
	if a.mode == keys.ModeInsert {
		if isEscape(ke) {
			a.exitInsert()
			a.mu.Unlock()
			return
		}
		switch ke.Name {
		case "Tab":
			if a.view == viewCompose {
				a.composeFocus = (a.composeFocus + 1) % 3
			}
			a.mu.Unlock()
			return
		}
		if ke.Modifiers.Contain(key.ModCtrl) && ke.Name == "S" {
			a.sendCompose(ctx)
			a.mu.Unlock()
			return
		}
		if ke.Modifiers.Contain(key.ModCtrl) && strings.EqualFold(string(ke.Name), "W") {
			var target *widget.Editor
			switch a.composeFocus {
			case 0:
				target = &a.to
			case 1:
				target = &a.subject
			default:
				target = &a.body
			}
			deleteLastWord(target)
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		return
	}
	if a.mode == keys.ModeSearch {
		if isEscape(ke) {
			a.mode = keys.ModeNormal
			a.status = ""
			a.mu.Unlock()
			return
		}
		switch ke.Name {
		case "⏎":
			a.mu.Unlock()
			a.runSearch(ctx)
			return
		}
		if ke.Modifiers.Contain(key.ModCtrl) && strings.EqualFold(string(ke.Name), "W") {
			deleteLastWord(&a.searchBuf)
			a.mu.Unlock()
			return
		}
		if ke.Modifiers.Contain(key.ModCtrl) || ke.Name == "↑" || ke.Name == "↓" {
			name := string(ke.Name)
			if strings.EqualFold(name, "N") || name == "↓" || (ke.Modifiers.Contain(key.ModCtrl) && strings.EqualFold(name, "J")) {
				if a.moveCursor(1) {
					a.openCurrent(ctx)
				}
				a.mu.Unlock()
				return
			}
			if strings.EqualFold(name, "P") || name == "↑" {
				if a.moveCursor(-1) {
					a.openCurrent(ctx)
				}
				a.mu.Unlock()
				return
			}
		}
		a.mu.Unlock()
		return
	}

	p := pressFromEvent(ke)
	if a.view == viewAccounts && p.Rune == 'h' && !p.Ctrl {
		a.mu.Unlock()
		return
	}
	act := a.binder.Resolve(keys.ModeNormal, p)
	if pend := a.binder.Pending(); pend != "" {
		a.status = pend
	}
	if act == keys.ActNone {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	a.run(ctx, act)
}

func pressFromEvent(ke key.Event) keys.Press {
	p := keys.Press{Ctrl: ke.Modifiers.Contain(key.ModCtrl)}
	if isEscape(ke) {
		p.Name = "Escape"
		return p
	}
	switch ke.Name {
	case "⏎", "⌤":
		p.Name = "Enter"
		return p
	case "Tab":
		p.Name = "Tab"
		return p
	case "Space":
		p.Name = "Space"
		return p
	case "←":
		p.Name = "←"
		return p
	case "→":
		p.Name = "→"
		return p
	case "↑":
		p.Name = "↑"
		return p
	case "↓":
		p.Name = "↓"
		return p
	}
	if len(ke.Name) == 1 {
		r := rune(ke.Name[0])
		// Gio reports key names in upper case; honor Shift modifier to
		// distinguish "g" from "G".
		if !ke.Modifiers.Contain(key.ModShift) && r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		p.Rune = r
	}
	return p
}

// ---------- action dispatch ----------

func (a *App) run(ctx context.Context, act keys.Action) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch act {
	case keys.ActQuit:
		a.status = "bye"
		go a.win.Perform(system.ActionClose)
	case keys.ActHelp:
		a.view = viewHelp
	case keys.ActSettings:
		if a.view == viewSettings {
			a.view = viewList
			a.status = ""
		} else {
			a.view = viewSettings
			a.mode = keys.ModeNormal
			a.status = "-- SETTINGS --"
		}
	case keys.ActDown:
		if a.view == viewLinks {
			a.moveLinkCursor(1)
		} else if a.focus == paneList {
			if a.moveCursor(1) {
				if a.view == viewList || a.view == viewMessage {
					a.openCurrent(ctx)
				}
			}
			a.checkLoadMore(ctx)
		} else {
			a.messageList.Position.Offset += 50
		}
	case keys.ActUp:
		if a.view == viewLinks {
			a.moveLinkCursor(-1)
		} else if a.focus == paneList {
			if a.moveCursor(-1) {
				if a.view == viewList || a.view == viewMessage {
					a.openCurrent(ctx)
				}
			}
		} else {
			a.messageList.Position.Offset -= 50
		}
	case keys.ActPageDown:
		if a.view == viewLinks {
			a.moveLinkCursor(10)
		} else if a.focus == paneList {
			if a.moveCursor(10) {
				a.openCurrent(ctx)
			}
			a.checkLoadMore(ctx)
		} else {
			a.messageList.Position.Offset += 500
		}
	case keys.ActPageUp:
		if a.view == viewLinks {
			a.moveLinkCursor(-10)
		} else if a.focus == paneList {
			if a.moveCursor(-10) {
				a.openCurrent(ctx)
			}
		} else {
			a.messageList.Position.Offset -= 500
		}
	case keys.ActTop:
		if a.view == viewLinks {
			a.linkCursor = 0
		} else if a.focus == paneList {
			a.cursor = 0
			a.openCurrent(ctx)
		} else {
			a.messageList.Position.First = 0
			a.messageList.Position.Offset = 0
		}
	case keys.ActBottom:
		if a.view == viewLinks {
			a.linkCursor = len(a.links) - 1
			if a.linkCursor < 0 {
				a.linkCursor = 0
			}
		} else if a.focus == paneList {
			max := len(a.items)
			if a.view == viewAccounts {
				max = len(a.accounts)
			}
			a.cursor = max - 1
			if a.cursor < 0 {
				a.cursor = 0
			}
			a.openCurrent(ctx)
			a.checkLoadMore(ctx)
		} else {
			// rough approximation
			a.messageList.Position.First = 1000
		}
	case keys.ActOpen:
		if a.view == viewAccounts {
			if a.cursor >= 0 && a.cursor < len(a.accounts) {
				email := a.accounts[a.cursor]
				go a.switchToAccount(ctx, email)
			}
		} else if a.view == viewLinks {
			if a.linkCursor >= 0 && a.linkCursor < len(a.links) {
				url := a.links[a.linkCursor].url
				go exec.Command("xdg-open", url).Start()
			}
		} else if a.view == viewList || a.view == viewMessage {
			if a.focus == paneList {
				a.focus = paneMessage
				a.openCurrent(ctx)
			} else {
				if a.message != nil {
					a.openLinksDialog()
				}
			}
		} else {
			a.openCurrent(ctx)
		}
	case keys.ActBack:
		switch a.view {
		case viewLinks:
			a.view = viewMessage
		case viewMessage, viewList:
			if a.focus == paneMessage {
				a.focus = paneList
			} else {
				go a.showAccounts(ctx)
			}
		case viewHelp, viewAccounts, viewSettings:
			a.view = viewList
		case viewCompose:
			a.view = viewList
			a.exitInsert()
		}
	case keys.ActArchive:
		a.archive(ctx)
	case keys.ActTrash:
		a.trash(ctx)
	case keys.ActStar:
		a.toggleStar(ctx)
	case keys.ActMarkRead:
		a.markRead(ctx, true)
	case keys.ActMarkUnread:
		a.markRead(ctx, false)
	case keys.ActCompose:
		a.startCompose("", "", "", "", "")
	case keys.ActReply:
		a.startReply(false)
	case keys.ActReplyAll:
		a.startReply(true)
	case keys.ActForward:
		a.startForward()
	case keys.ActSearch:
		a.mode = keys.ModeSearch
		a.searchBuf.SetText("")
		a.status = "/"
	case keys.ActRefresh:
		go a.refresh(ctx)
	case keys.ActGotoInbox:
		a.folderIdx = 0
		a.listMax = 50
		go a.refresh(ctx)
	case keys.ActGotoStarred:
		a.folderIdx = 1
		a.listMax = 50
		go a.refresh(ctx)
	case keys.ActGotoSent:
		a.folderIdx = 2
		a.listMax = 50
		go a.refresh(ctx)
	case keys.ActGotoTrash:
		a.folderIdx = 3
		a.listMax = 50
		go a.refresh(ctx)
	case keys.ActSend:
		a.sendCompose(ctx)
	case keys.ActSwitchAccount:
		go a.cycleAccount(ctx)
	case keys.ActEnterInsert:
		if a.view == viewCompose {
			a.mode = keys.ModeInsert
			a.status = "-- INSERT --"
		}
	}
}

func (a *App) moveCursor(d int) bool {
	max := len(a.items)
	if a.view == viewAccounts {
		max = len(a.accounts)
	}
	if max == 0 {
		return false
	}
	old := a.cursor
	a.cursor += d
	if a.cursor < 0 {
		a.cursor = 0
	}
	if a.cursor >= max {
		a.cursor = max - 1
	}
	return a.cursor != old
}

func (a *App) checkLoadMore(ctx context.Context) {
	if a.view != viewList && a.view != viewMessage {
		return
	}
	if a.loading || len(a.items) == 0 {
		return
	}
	if a.cursor >= len(a.items)-5 && int64(len(a.items)) >= a.listMax {
		a.listMax += 50
		go a.refresh(ctx)
	}
}

func (a *App) moveLinkCursor(d int) {
	max := len(a.links)
	if max == 0 {
		return
	}
	a.linkCursor += d
	if a.linkCursor < 0 {
		a.linkCursor = 0
	}
	if a.linkCursor >= max {
		a.linkCursor = max - 1
	}
}

func (a *App) openLinksDialog() {
	var links []linkItem
	seen := make(map[string]int)
	for _, s := range a.message.Body {
		if s.URL != "" {
			text := strings.TrimSpace(s.Text)
			text = strings.ReplaceAll(text, "\n", " ")
			if text == "" {
				text = "(no text)"
			}
			if idx, ok := seen[s.URL]; ok {
				if text != "(no text)" && !strings.Contains(links[idx].text, text) {
					if links[idx].text == "(no text)" {
						links[idx].text = text
					} else {
						links[idx].text += ", " + text
					}
				}
			} else {
				seen[s.URL] = len(links)
				links = append(links, linkItem{text: text, url: s.URL})
			}
		}
	}
	if len(links) == 0 {
		a.status = "no links in message"
		return
	}
	a.links = links
	a.linkCursor = 0
	a.linkScroll = 0
	a.view = viewLinks
	a.status = "select a link to open in browser"
	a.win.Invalidate()
}

// ---------- async ops ----------

// cycleAccount swaps the live Gmail client to the next registered account.
// With only one account it's a no-op (with a status hint).
func (a *App) cycleAccount(ctx context.Context) {
	if a.switchTo == nil || a.listAccounts == nil {
		return
	}
	accounts, err := a.listAccounts()
	if err != nil {
		a.setStatus("accounts: " + err.Error())
		return
	}
	if len(accounts) < 2 {
		a.setStatus("only one account registered — run wlmail -add for more")
		return
	}
	a.mu.Lock()
	cur := a.email
	a.mu.Unlock()
	idx := 0
	for i, e := range accounts {
		if e == cur {
			idx = i
			break
		}
	}
	next := accounts[(idx+1)%len(accounts)]
	go a.switchToAccount(ctx, next)
}

func (a *App) showAccounts(ctx context.Context) {
	if a.listAccounts == nil {
		return
	}
	accounts, err := a.listAccounts()
	if err != nil {
		a.setStatus("accounts: " + err.Error())
		return
	}
	a.mu.Lock()
	a.accounts = accounts
	a.view = viewAccounts
	a.cursor = 0
	for i, e := range accounts {
		if e == a.email {
			a.cursor = i
			break
		}
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

func (a *App) switchToAccount(ctx context.Context, email string) {
	client, err := a.switchTo(email)
	if err != nil {
		a.setStatus("switch failed: " + err.Error())
		return
	}
	a.mu.Lock()
	a.client = client
	a.email = email
	a.message = nil
	a.items = nil
	a.cursor = 0
	a.scroll = 0
	a.listMax = 50
	a.searchQ = ""
	a.view = viewList
	a.status = "switched to " + email
	a.mu.Unlock()
	a.refresh(ctx)

	a.mu.Lock()
	if len(a.items) > 0 {
		a.openCurrent(ctx)
	}
	a.mu.Unlock()
}

func (a *App) refresh(ctx context.Context) {
	a.mu.Lock()
	a.loading = true
	q := folders[a.folderIdx].query
	if a.searchQ != "" {
		q = a.searchQ
	}
	folderName := folders[a.folderIdx].name
	a.mu.Unlock()

	a.setStatus(fmt.Sprintf("checking %s…", strings.ToLower(folderName)))

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	a.mu.Lock()
	maxItems := a.listMax
	a.mu.Unlock()

	items, err := a.client.List(cctx, q, int64(maxItems))

	a.mu.Lock()
	a.loading = false
	if err != nil {
		a.status = "error: " + err.Error()
	} else {
		a.items = items
		if a.cursor >= len(items) {
			a.cursor = 0
		}
		unread := 0
		for _, it := range items {
			if it.Unread {
				unread++
			}
		}
		now := time.Now().Format("15:04")
		if unread > 0 {
			a.status = fmt.Sprintf("%s — %d messages (%d unread) — %s", folderName, len(items), unread, now)
		} else {
			a.status = fmt.Sprintf("%s — %d messages — %s", folderName, len(items), now)
		}
	}
	a.mu.Unlock()
	a.win.Invalidate()
}

func (a *App) setStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
	a.win.Invalidate()
}

// openCurrent kicks off an async fetch of the message under the cursor.
// Caller must hold a.mu — we read a.cursor/a.items synchronously, then the
// goroutine re-acquires the lock to publish the result.
func (a *App) openCurrent(ctx context.Context) {
	if a.cursor < 0 || a.cursor >= len(a.items) {
		return
	}
	id := a.items[a.cursor].ID
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		m, err := a.client.Get(cctx, id)
		a.mu.Lock()
		if err != nil {
			a.status = "open failed: " + err.Error()
		} else {
			a.message = m
			a.view = viewMessage
			if m.Unread {
				go func() { _ = a.client.MarkRead(ctx, m.ID) }()
			}
		}
		a.mu.Unlock()
		a.win.Invalidate()
	}()
}

func (a *App) currentID() string {
	if a.view == viewMessage && a.message != nil {
		return a.message.ID
	}
	if a.cursor >= 0 && a.cursor < len(a.items) {
		return a.items[a.cursor].ID
	}
	return ""
}

func (a *App) archive(ctx context.Context) {
	id := a.currentID()
	if id == "" {
		return
	}
	go func() {
		if err := a.client.Archive(ctx, id); err != nil {
			a.setStatus("archive failed: " + err.Error())
			return
		}
		a.setStatus("archived")
		a.refresh(ctx)
	}()
}

func (a *App) trash(ctx context.Context) {
	id := a.currentID()
	if id == "" {
		return
	}
	go func() {
		if err := a.client.Trash(ctx, id); err != nil {
			a.setStatus("trash failed: " + err.Error())
			return
		}
		a.setStatus("trashed")
		a.refresh(ctx)
	}()
}

func (a *App) toggleStar(ctx context.Context) {
	if a.cursor < 0 || a.cursor >= len(a.items) {
		return
	}
	it := &a.items[a.cursor]
	currentlyStarred := it.Starred
	go func() {
		if err := a.client.ToggleStar(ctx, it.ID, currentlyStarred); err != nil {
			a.setStatus("star failed: " + err.Error())
			return
		}
		a.mu.Lock()
		it.Starred = !currentlyStarred
		a.mu.Unlock()
		a.win.Invalidate()
	}()
}

func (a *App) markRead(ctx context.Context, read bool) {
	if a.cursor < 0 || a.cursor >= len(a.items) {
		return
	}
	id := a.items[a.cursor].ID
	go func() {
		var err error
		if read {
			err = a.client.MarkRead(ctx, id)
		} else {
			err = a.client.MarkUnread(ctx, id)
		}
		if err != nil {
			a.setStatus("mark failed: " + err.Error())
			return
		}
		a.refresh(ctx)
	}()
}

func (a *App) runSearch(ctx context.Context) {
	a.mu.Lock()
	q := strings.TrimSpace(a.searchBuf.Text())
	a.searchQ = q
	a.listMax = 50
	a.focus = paneList
	a.mode = keys.ModeNormal
	a.mu.Unlock()
	go a.refresh(ctx)
}

// ---------- compose ----------

func (a *App) startCompose(to, subject, body, replyTo, refs string) {
	a.to.SetText(to)
	a.subject.SetText(subject)
	a.body.SetText(body)
	a.composeReplyToID = replyTo
	a.composeReferences = refs
	a.composeThreadID = ""
	if a.message != nil && replyTo != "" {
		a.composeThreadID = a.message.ThreadID
	}
	a.view = viewCompose
	a.composeFocus = 2
	a.mode = keys.ModeInsert
	a.status = "-- INSERT --   <C-s> send  <Esc> normal"
}

func (a *App) startReply(all bool) {
	if a.message == nil {
		return
	}
	to := a.message.From
	cc := ""
	if all {
		cc = a.message.Cc
	}
	subject := a.message.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	quoted := quoteBody(a.message)
	refs := strings.TrimSpace(a.message.Headers["References"] + " " + a.message.Headers["Message-ID"])
	a.startCompose(to, subject, "\n\n"+quoted, a.message.Headers["Message-ID"], refs)
	if cc != "" {
		// stash Cc in subject prefix for now — keep MVP simple
		a.status = "reply-all (Cc: " + cc + ")"
	}
}

func (a *App) startForward() {
	if a.message == nil {
		return
	}
	subject := a.message.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "fwd:") {
		subject = "Fwd: " + subject
	}
	body := "\n\n---------- Forwarded message ----------\n" +
		"From: " + a.message.From + "\n" +
		"Subject: " + a.message.Subject + "\n\n" +
		a.message.Text()
	a.startCompose("", subject, body, "", "")
}

func quoteBody(m *mail.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "On %s, %s wrote:\n", m.Date.Format("Mon Jan 2 15:04"), m.From)
	for _, line := range strings.Split(m.Text(), "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (a *App) sendCompose(ctx context.Context) {
	if a.view != viewCompose {
		return
	}
	out := mail.Outgoing{
		To:         a.to.Text(),
		Subject:    a.subject.Text(),
		Body:       a.body.Text(),
		InReplyTo:  a.composeReplyToID,
		References: a.composeReferences,
		ThreadID:   a.composeThreadID,
	}
	go func() {
		a.setStatus("sending…")
		if err := a.client.Send(ctx, out); err != nil {
			a.setStatus("send failed: " + err.Error())
			return
		}
		a.mu.Lock()
		a.view = viewList
		a.mode = keys.ModeNormal
		a.status = "sent"
		a.mu.Unlock()
		a.win.Invalidate()
	}()
}

func (a *App) exitInsert() {
	a.mode = keys.ModeNormal
	a.status = ""
}

func (a *App) loadImage(url string) {
	a.imgMu.Lock()
	if _, ok := a.imgCache[url]; ok {
		a.imgMu.Unlock()
		return
	}
	// Use a placeholder while loading to avoid multiple requests
	a.imgCache[url] = paint.ImageOp{}
	a.imgMu.Unlock()

	go func() {
		resp, err := http.Get(url)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}

		img, _, err := image.Decode(resp.Body)
		if err != nil {
			return
		}

		op := paint.NewImageOp(img)
		a.imgMu.Lock()
		a.imgCache[url] = op
		a.imgMu.Unlock()
		a.win.Invalidate()
	}()
}
