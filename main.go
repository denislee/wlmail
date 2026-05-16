// wlmail is a Wayland-native Gmail client with vim keybindings.
//
//	wlmail               # GUI for the active account
//	wlmail -add          # register a new account (runs OAuth in your browser)
//	wlmail -list         # list registered accounts (active marked with *)
//	wlmail -use <email>  # set the active account
//	wlmail -rm <email>   # remove an account
//	wlmail -account <email>  # use this account for one session only
//
// Place your Google OAuth client JSON at ~/.config/wlmail/credentials.json
// before launching. Tokens are stored per-account under that same directory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gioui.org/app"

	"wlmail/internal/auth"
	"wlmail/internal/cache"
	"wlmail/internal/mail"
	"wlmail/internal/ui"
)

type uiSettings struct {
	DefaultAccount string `json:"DefaultAccount"`
}

func loadUISettings() uiSettings {
	var s uiSettings
	base, err := os.UserConfigDir()
	if err != nil {
		return s
	}
	b, err := os.ReadFile(filepath.Join(base, "wlmail", "settings.json"))
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func main() {
	log.SetOutput(os.Stdout)
	var (
		addFlag     = flag.Bool("add", false, "register a new Google account via OAuth and exit")
		listFlag    = flag.Bool("list", false, "list registered accounts and exit")
		useFlag     = flag.String("use", "", "set this account as the default and exit")
		rmFlag      = flag.String("rm", "", "remove this account and exit")
		accountFlag = flag.String("account", "", "use this account for the current session (does not change the default)")
	)
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch {
	case *addFlag:
		email, err := auth.Add(ctx)
		if err != nil {
			log.Fatalf("add: %v", err)
		}
		fmt.Printf("registered %s as the active account\n", email)
		return
	case *listFlag:
		listAccounts()
		return
	case *useFlag != "":
		if err := auth.SetActive(*useFlag); err != nil {
			log.Fatalf("use: %v", err)
		}
		fmt.Printf("active account is now %s\n", *useFlag)
		return
	case *rmFlag != "":
		if err := auth.Remove(*rmFlag); err != nil {
			log.Fatalf("rm: %v", err)
		}
		fmt.Printf("removed %s\n", *rmFlag)
		return
	}

	email := *accountFlag
	if email == "" {
		// Try to load UI settings first to see if a default account is set there
		if s := loadUISettings(); s.DefaultAccount != "" {
			email = s.DefaultAccount
		} else {
			var err error
			email, err = auth.Active()
			if err != nil {
				log.Fatalf("auth: %v", err)
			}
		}
	}

	var (
		hc  *http.Client
		err error
	)
	if email == "" {
		hc, err = auth.Client(ctx) // first-run: triggers Add
		if err != nil {
			log.Fatalf("auth: %v", err)
		}
		email, _ = auth.Active()
	} else {
		hc, err = auth.ClientFor(ctx, email)
		if err != nil {
			log.Fatalf("auth: %v", err)
		}
	}

	client, err := openClient(ctx, email, hc)
	if err != nil {
		log.Fatalf("gmail: %v", err)
	}

	go func() {
		if err := ui.Run(ctx, ui.Config{
			Email:        email,
			Client:       client,
			SwitchTo:     switchAccount(ctx),
			Reauth:       reauthAccount(ctx),
			IsAuthErr:    auth.IsAuthExpired,
			ListAccounts: auth.List,
		}); err != nil {
			log.Printf("ui: %v", err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// openClient builds a mail.Client wrapped by a per-account SQLite cache.
// The cache DB lives at ~/.config/wlmail/accounts/<email>/cache.db.
func openClient(ctx context.Context, email string, hc *http.Client) (ui.Client, error) {
	api, err := mail.New(ctx, hc)
	if err != nil {
		return nil, err
	}
	base, err := auth.ConfigDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "accounts", email)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return cache.Open(dir, api)
}

func listAccounts() {
	accounts, err := auth.List()
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(accounts) == 0 {
		fmt.Println("no accounts registered — run 'wlmail -add' to register one")
		return
	}
	active, _ := auth.Active()
	for _, a := range accounts {
		marker := " "
		if a == active {
			marker = "*"
		}
		fmt.Printf("%s %s\n", marker, a)
	}
}

// switchAccount returns a callback the UI can invoke to swap the live
// client (cache + Gmail API) to a different registered account.
func switchAccount(ctx context.Context) func(string) (ui.Client, error) {
	return func(email string) (ui.Client, error) {
		hc, err := auth.ClientFor(ctx, email)
		if err != nil {
			return nil, err
		}
		return openClient(ctx, email, hc)
	}
}

// reauthAccount returns a callback the UI invokes when the stored
// refresh token has been revoked. It runs OAuth in the browser, then
// rebuilds the mail client around the freshly-saved token.
func reauthAccount(ctx context.Context) func(string) (ui.Client, error) {
	return func(email string) (ui.Client, error) {
		if err := auth.Reauth(ctx, email); err != nil {
			return nil, err
		}
		hc, err := auth.ClientFor(ctx, email)
		if err != nil {
			return nil, err
		}
		return openClient(ctx, email, hc)
	}
}
