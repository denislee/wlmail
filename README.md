# wlmail

A Wayland-native Gmail client written in Go, driven entirely by vim-style
keybindings. Built on [Gio](https://gioui.org) (pure-Go GPU GUI) and the
official Gmail API.

## Features

- Native Wayland window via Gio (also runs on X11 / macOS / Windows)
- OAuth2 with token persisted in `$XDG_CONFIG_HOME/wlmail/`
- Vim navigation: `j`/`k`, `gg`/`G`, `Ctrl-d`/`Ctrl-u`, `/` to search
- Actions: `e` archive, `dd` trash, `s` star, `r`/`a`/`f` reply / reply-all / forward, `c` compose
- Folder switching: `gi` inbox, `gs` starred, `gt` sent, `gT` trash, `gu` unread
- Compose mode: `i` to enter insert, `Esc` to leave, `Ctrl-s` to send
- `?` opens a built-in cheat sheet

## One-time setup: Google OAuth client

wlmail uses Google's OAuth flow, which always requires a `client_id`. There
are two ways to supply one — pick whichever fits your distribution model.

### Option A: bake it into the binary (recommended for personal builds)

1. Create a project in the [Google Cloud Console](https://console.cloud.google.com/).
2. Enable the **Gmail API**.
3. Configure the OAuth consent screen (External / Testing is fine; add your address as a test user).
4. Create credentials → **OAuth 2.0 Client ID** → Application type **Desktop app**.
5. Drop a `.env` file at the repo root with:

   ```
   CLIENT_ID=...apps.googleusercontent.com
   CLIENT_SECRET=...
   ```

   (`.env` is already `.gitignore`d.) Or `export` those vars in your shell.
6. `make build` injects them via `-ldflags`. Verify with `make release`,
   which fails loudly if either value is empty.

The `client_secret` for a Desktop OAuth client is *not actually secret* per
[Google's docs](https://developers.google.com/identity/protocols/oauth2/native-app)
— it's expected to ship in installed apps. The protection is the loopback
redirect plus user consent.

### Option B: per-user credentials.json

If you don't want to embed credentials, drop the OAuth client JSON at
`~/.config/wlmail/credentials.json`. This always wins over the embedded
defaults — useful for users who want their own quota / project.

### After setup

```
./wlmail -add
```

opens a browser tab for consent; the resulting refresh token is cached at
`~/.config/wlmail/accounts/<email>/token.json`.

## Build & run

```
make run
# or:
go build -o wlmail . && ./wlmail
```

## Multiple Google accounts

`credentials.json` is shared across all accounts (it's the OAuth client, not
account-specific). Each account's refresh token is stored under
`~/.config/wlmail/accounts/<email>/token.json`, and the active account is
recorded in `~/.config/wlmail/accounts.json`.

```
wlmail -add               # register a new account; opens browser, becomes active
wlmail -list              # show registered accounts (* marks active)
wlmail -use you@gmail.com # set default account
wlmail -account you@…     # use this account just for this session
wlmail -rm  you@gmail.com # forget an account (deletes its token)
```

In the UI, press `ga` to cycle to the next registered account. The active
email is shown in the top-right corner.

Wayland is auto-detected; force it with `GDK_BACKEND=wayland` or run under a
Wayland compositor (sway, Hyprland, GNOME, KDE Plasma 5.24+).

## Keybindings

| Key            | Action                       |
|----------------|------------------------------|
| `j` / `k`      | next / prev message          |
| `gg` / `G`     | top / bottom of list         |
| `Ctrl-d/u`     | page down / up               |
| `Enter`, `l`   | open message                 |
| `h`, `Esc`     | back to list                 |
| `e`            | archive (remove from inbox)  |
| `dd`           | move to trash                |
| `s`            | toggle star                  |
| `u` / `U`      | mark unread / read           |
| `c`            | compose                      |
| `r` / `a` / `f`| reply / reply-all / forward  |
| `R`, `Ctrl-r`  | refresh current folder       |
| `/`            | start search (Enter to run)  |
| `n` / `N`      | next / prev match            |
| `gi gs gt gT gu` | inbox / starred / sent / trash / unread |
| `ga`           | switch to next Google account  |
| `i`            | (compose) enter insert mode  |
| `Ctrl-s`       | (compose) send               |
| `?`            | help                         |
| `q`            | quit                         |

## Project layout

```
main.go                    entrypoint, OAuth bootstrap
internal/auth              OAuth2 loopback flow, token storage
internal/cache             SQLite per-account cache (read- + write-through)
internal/mail              Gmail API wrapper (list/get/modify/send)
internal/keys              vim keybinding state machine
internal/ui                Gio UI: list / message / compose / help views
```

## Local cache

Each account has its own SQLite database at
`~/.config/wlmail/accounts/<email>/cache.db`. Lists for the four built-in
folders (inbox / starred / sent / trash) render from cache instantly and
trigger a background refresh; arbitrary searches always go to the API.
Message bodies are cached on first open so re-reading the same thread is
free. Modifications (archive / trash / star / mark read) write through to
the API and update the cache only on success.

If you ever want a clean slate, delete the file — the cache will rebuild
on the next launch.
