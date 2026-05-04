package auth

// Default OAuth client credentials baked into the binary at build time.
//
// Inject via -ldflags, e.g.:
//
//	go build -ldflags "
//	  -X wlmail/internal/auth.embeddedClientID=YOUR_ID.apps.googleusercontent.com
//	  -X wlmail/internal/auth.embeddedClientSecret=YOUR_SECRET" .
//
// Per Google's installed-app guidance, the "client_secret" of a Desktop OAuth
// client is not actually a secret — it's expected to ship with the binary.
// See https://developers.google.com/identity/protocols/oauth2/native-app.
//
// A user-supplied ~/.config/wlmail/credentials.json always overrides these.
var (
	embeddedClientID     = ""
	embeddedClientSecret = ""
)
