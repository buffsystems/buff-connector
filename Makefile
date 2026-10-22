# Connector agent — build, test & distribution.
# Requires Go 1.22+. `make dist` cross-compiles every target from one build;
# `make sign` cosign-signs the artifacts (keyless, in CI with an OIDC identity).

GO ?= go
PKG := ./...
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# RELAY overrides the baked-in default middleware address (config.DefaultRelay) at
# build time — e.g. make dist RELAY=wss://relay.mycloud/nats
RELAY ?=
CFGPKG := github.com/buffsystems/buff-connector/internal/config
PLUGPKG := github.com/buffsystems/buff-connector/internal/plugin
# The publisher keys a release trusts. Baked in, not read from the environment:
# what a connector will install must not depend on a variable anyone on the
# machine can set. It is a SET — a new key is trusted alongside the old one long
# before the old one goes away, because instances stay pinned to old versions.
TRUSTED_KEYS ?= baQlLzFyriz+3K21Z2LsLdgqQ+hFcnjASqLAgmRXUkg=
LDFLAGS := -s -w -X main.version=$(VERSION) -X $(PLUGPKG).trustedKeysCSV=$(TRUSTED_KEYS)$(if $(RELAY), -X $(CFGPKG).DefaultRelay=$(RELAY))$(if $(CATALOG), -X main.catalogBase=$(CATALOG))
DIST := dist

# os/arch matrix — one build produces all of them.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: test vet fmt-check tidy build clean dist checksums sign docker ui ui-check

# The local page's look is the Buff design system (#561): the package's plain CSS (fonts,
# tokens, base, composite ui-* classes) is copied into internal/admin/ui and embedded, so
# `go build` needs no node. Bump the tag in package.json, then `make ui`; ui-check (CI)
# fails when the committed copy is not the pinned package's. The package is public
# (github.com/buffsystems/buff-ui), so npm needs no credentials.
UI_DIR := internal/admin/ui

ui:
	npm ci --no-audit --no-fund --loglevel=error
	rm -rf $(UI_DIR) && cp -r node_modules/@buff/ui/styles $(UI_DIR)
	rm -f $(UI_DIR)/tailwind.css $(UI_DIR)/theme.css

ui-check: ui
	@git diff --quiet --exit-code -- $(UI_DIR) && test -z "$$(git ls-files --others --exclude-standard -- $(UI_DIR))" \
	  || { echo "$(UI_DIR) is not the pinned @buff/ui — run make ui and commit"; exit 1; }

test:
	$(GO) test $(PKG)

vet:
	$(GO) vet $(PKG)

fmt-check:
	@out=$$(gofmt -l . ); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	$(GO) mod tidy

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o connector ./cmd/connector

# Cross-compile every platform into dist/ (static, CGO off), then checksum.
dist: clean
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
	  out=$(DIST)/connector-$$os-$$arch$$ext; \
	  echo "build $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/connector || exit 1; \
	done
	@$(MAKE) checksums

checksums:
	@cd $(DIST) && (sha256sum connector-* > SHA256SUMS 2>/dev/null || shasum -a 256 connector-* > SHA256SUMS) && cat SHA256SUMS

# Detached cosign signatures over every artifact, with the release key. This is
# the format the download page publishes and the install page tells a customer to
# check — `cosign verify-blob --key cosign.pub --signature <f>.sig <f>` — so the
# shape here is a contract with people who already scripted that check, not a
# free choice. The signature also goes to the public transparency log, because
# that plain command REQUIRES it: sign with --tlog-upload=false and every customer
# following the install page gets "signature not found in transparency log".
# cosign 2.x; KEY is passed in and never lives in this repo. The glob skips
# existing .sig files: signing twice without a rebuild otherwise produces
# connector-linux-amd64.sig.sig, and those get published alongside the real ones.
COSIGN ?= cosign
KEY ?=
sign:
	@[ -n "$(KEY)" ] || { echo "KEY=/path/to/cosign.key is required"; exit 1; }
	@command -v $(COSIGN) >/dev/null 2>&1 || [ -x "$(COSIGN)" ] || { echo "cosign not found: $(COSIGN)"; exit 1; }
	@key=$$(cd $$(dirname $(KEY)) && pwd)/$$(basename $(KEY)); \
	cd $(DIST) && for f in $$(ls connector-* SHA256SUMS | grep -v '\.sig$$'); do \
	  echo "sign $$f"; \
	  $(COSIGN) sign-blob --yes --key $$key --output-signature $$f.sig $$f || exit 1; \
	done

# Multi-arch image: packages the `make dist` binaries (COPY-only) and pushes a
# manifest list. Needs buildx + a `docker login $(IMAGE)`. Override IMAGE for a
# different registry.
IMAGE ?= registry.buff.systems/buff/connector
docker: dist
	docker buildx build --platform linux/amd64,linux/arm64 \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

clean:
	rm -f connector
	rm -rf $(DIST)

# Our own release operations live beside, outside the public source (#624).
-include ops.mk
