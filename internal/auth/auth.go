// Package auth handles OAuth2 + multi-account token storage.
//
// Layout under $XDG_CONFIG_HOME/wlmail (typically ~/.config/wlmail):
//
//	credentials.json                     shared OAuth client
//	accounts.json                        {active, accounts[]} index
//	accounts/<email>/token.json          per-account refresh token
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const (
	credentialsFile = "credentials.json"
	indexFile       = "accounts.json"
	tokenFile       = "token.json"
)

// Index is the on-disk record of known accounts.
type Index struct {
	Active   string   `json:"active"`
	Accounts []string `json:"accounts"`
}

// ConfigDir returns the wlmail config directory, creating it if missing.
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "wlmail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func accountDir(base, email string) string {
	return filepath.Join(base, "accounts", email)
}

// ---------- credentials / index ----------

var oauthScopes = []string{
	gmail.GmailModifyScope,
	gmail.GmailSendScope,
	gmail.GmailComposeScope,
}

// loadCredentials returns the OAuth client config. A user-supplied
// credentials.json always wins; otherwise we fall back to the values
// baked into the binary at build time.
func loadCredentials(base string) (*oauth2.Config, error) {
	path := filepath.Join(base, credentialsFile)
	b, err := os.ReadFile(path)
	if err == nil {
		cfg, perr := google.ConfigFromJSON(b, oauthScopes...)
		if perr != nil {
			return nil, fmt.Errorf("parse credentials: %w", perr)
		}
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	if embeddedClientID != "" && embeddedClientSecret != "" {
		return &oauth2.Config{
			ClientID:     embeddedClientID,
			ClientSecret: embeddedClientSecret,
			Endpoint:     google.Endpoint,
			Scopes:       oauthScopes,
		}, nil
	}
	return nil, fmt.Errorf(
		"no OAuth client configured: place credentials.json at %s, "+
			"or rebuild with -ldflags injecting embeddedClientID/embeddedClientSecret",
		path,
	)
}

func loadIndex(base string) (*Index, error) {
	path := filepath.Join(base, indexFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Index{}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

func saveIndex(base string, idx *Index) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, indexFile), b, 0o600)
}

// List returns the registered account emails.
func List() ([]string, error) {
	base, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return nil, err
	}
	return idx.Accounts, nil
}

// Active returns the currently selected account email, or "" if none.
func Active() (string, error) {
	base, err := ConfigDir()
	if err != nil {
		return "", err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return "", err
	}
	return idx.Active, nil
}

// SetActive selects an existing account as the default.
func SetActive(email string) error {
	base, err := ConfigDir()
	if err != nil {
		return err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return err
	}
	if !contains(idx.Accounts, email) {
		return fmt.Errorf("unknown account %q (run with -add to register)", email)
	}
	idx.Active = email
	return saveIndex(base, idx)
}

// Remove deletes an account's stored token and index entry.
func Remove(email string) error {
	base, err := ConfigDir()
	if err != nil {
		return err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return err
	}
	idx.Accounts = without(idx.Accounts, email)
	if idx.Active == email {
		idx.Active = ""
		if len(idx.Accounts) > 0 {
			idx.Active = idx.Accounts[0]
		}
	}
	if err := saveIndex(base, idx); err != nil {
		return err
	}
	return os.RemoveAll(accountDir(base, email))
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func without(xs []string, v string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// ---------- token storage ----------

func loadToken(base, email string) (*oauth2.Token, error) {
	b, err := os.ReadFile(filepath.Join(accountDir(base, email), tokenFile))
	if err != nil {
		return nil, err
	}
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func saveToken(base, email string, t *oauth2.Token) error {
	dir := accountDir(base, email)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tokenFile), b, 0o600)
}

// ---------- OAuth flow ----------

func randState() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return
	}
	_ = cmd.Start()
}

// interactive runs the loopback OAuth flow against cfg.
func interactive(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	state := randState()
	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	var once sync.Once

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("state") != state {
				http.Error(w, "state mismatch", http.StatusBadRequest)
				once.Do(func() { resCh <- result{err: fmt.Errorf("state mismatch")} })
				return
			}
			if e := q.Get("error"); e != "" {
				http.Error(w, e, http.StatusBadRequest)
				once.Do(func() { resCh <- result{err: fmt.Errorf("oauth error: %s", e)} })
				return
			}
			fmt.Fprintln(w, "<html><body><h2>wlmail authorized — you may close this tab.</h2></body></html>")
			once.Do(func() { resCh <- result{code: q.Get("code")} })
		}),
	}
	go srv.Serve(ln)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	fmt.Printf("Open this URL to authorize wlmail:\n  %s\n", url)
	openBrowser(url)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		return cfg.Exchange(ctx, r.code)
	}
}

// fetchEmail asks Gmail for the user's primary address.
func fetchEmail(ctx context.Context, hc *http.Client) (string, error) {
	svc, err := gmail.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return "", err
	}
	p, err := svc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if p.EmailAddress == "" {
		return "", fmt.Errorf("empty email address from Gmail profile")
	}
	return p.EmailAddress, nil
}

// Add runs the OAuth flow, looks up the user's email, and registers the
// account in the index (making it active). Returns the email.
func Add(ctx context.Context) (string, error) {
	base, err := ConfigDir()
	if err != nil {
		return "", err
	}
	cfg, err := loadCredentials(base)
	if err != nil {
		return "", err
	}
	tok, err := interactive(ctx, cfg)
	if err != nil {
		return "", err
	}
	hc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	email, err := fetchEmail(ctx, hc)
	if err != nil {
		return "", fmt.Errorf("fetch email: %w", err)
	}
	if err := saveToken(base, email, tok); err != nil {
		return "", err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return "", err
	}
	if !contains(idx.Accounts, email) {
		idx.Accounts = append(idx.Accounts, email)
	}
	idx.Active = email
	if err := saveIndex(base, idx); err != nil {
		return "", err
	}
	return email, nil
}

// Reauth runs the OAuth flow for an already-registered account and
// overwrites its stored token. It verifies the user authorized the
// same Google account so we don't silently clobber another account's
// tokens if the wrong identity is chosen in the browser.
func Reauth(ctx context.Context, email string) error {
	base, err := ConfigDir()
	if err != nil {
		return err
	}
	cfg, err := loadCredentials(base)
	if err != nil {
		return err
	}
	tok, err := interactive(ctx, cfg)
	if err != nil {
		return err
	}
	hc := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	got, err := fetchEmail(ctx, hc)
	if err != nil {
		return fmt.Errorf("fetch email: %w", err)
	}
	if got != email {
		return fmt.Errorf("authorized %s, expected %s — token discarded", got, email)
	}
	return saveToken(base, email, tok)
}

// IsAuthExpired reports whether err indicates the OAuth refresh token
// has been revoked or expired (invalid_grant). When true, the caller
// should re-run the OAuth flow to recover.
func IsAuthExpired(err error) bool {
	if err == nil {
		return false
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return re.ErrorCode == "invalid_grant"
	}
	s := err.Error()
	return strings.Contains(s, "invalid_grant") ||
		strings.Contains(s, "Token has been expired or revoked")
}

// ---------- HTTP clients ----------

// ClientFor returns an authenticated HTTP client for the named account.
// Refreshed tokens are written back to disk.
func ClientFor(ctx context.Context, email string) (*http.Client, error) {
	base, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	cfg, err := loadCredentials(base)
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(base, email)
	if err != nil {
		return nil, fmt.Errorf("load token for %s: %w", email, err)
	}
	src := cfg.TokenSource(ctx, tok)
	return oauth2.NewClient(ctx, &savingSource{base: base, email: email, src: src, last: tok}), nil
}

// Client returns the active account's HTTP client, prompting for OAuth if no
// account is registered yet.
func Client(ctx context.Context) (*http.Client, error) {
	base, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	idx, err := loadIndex(base)
	if err != nil {
		return nil, err
	}
	if idx.Active == "" || !contains(idx.Accounts, idx.Active) {
		email, err := Add(ctx)
		if err != nil {
			return nil, err
		}
		return ClientFor(ctx, email)
	}
	return ClientFor(ctx, idx.Active)
}

type savingSource struct {
	mu    sync.Mutex
	base  string
	email string
	src   oauth2.TokenSource
	last  *oauth2.Token
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	if s.last == nil || t.AccessToken != s.last.AccessToken || !t.Expiry.Equal(s.last.Expiry) {
		_ = saveToken(s.base, s.email, t)
		s.last = t
	}
	return t, nil
}
