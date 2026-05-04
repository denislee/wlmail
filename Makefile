BIN     := wlmail
PKG     := .
GOFLAGS ?=

# Embed default OAuth client credentials into the binary.
#
# Either:
#   - export CLIENT_ID and CLIENT_SECRET before running make, or
#   - drop a .env file in this directory containing:
#         CLIENT_ID=...apps.googleusercontent.com
#         CLIENT_SECRET=...
#
# A user-supplied ~/.config/wlmail/credentials.json always overrides these.
-include .env
export CLIENT_ID
export CLIENT_SECRET

LDFLAGS := \
	-X wlmail/internal/auth.embeddedClientID=$(CLIENT_ID) \
	-X wlmail/internal/auth.embeddedClientSecret=$(CLIENT_SECRET)

.PHONY: all build run test tidy vet clean check-creds

all: build

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

run: build
	./$(BIN)

# Like `build`, but fail loudly if no client credentials were provided.
release: check-creds build

check-creds:
	@if [ -z "$(CLIENT_ID)" ] || [ -z "$(CLIENT_SECRET)" ]; then \
		echo "CLIENT_ID and CLIENT_SECRET must be set (export them or drop a .env file)"; \
		exit 1; \
	fi

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)
