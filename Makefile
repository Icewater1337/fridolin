# Build, test and lint Fridolin. CI's build, lint and test jobs run these
# same targets, so a passing `make check` here should mean those pass in CI
# too (on this OS). CI also runs the site's tests (site/, which need its
# npm packages), the fuzz targets on pushes to main (fuzz-short runs them
# here), govulncheck, CodeQL and the personal-data scan of the pushed
# commits; make check does not.
#
#   make build        ./fridolin
#   make check        what CI's build, lint and test jobs run
#   make help         every target

# The main package is at the repo root or in ./cmd/fridolin; build whichever
# this checkout has.
MAIN_PKG := $(if $(wildcard cmd/fridolin/*.go),./cmd/fridolin,.)
BINARY   ?= fridolin

GO     ?= go
PYTHON ?= python3
NODE   ?= node

# make install copies the binary here; the service setup.sh installs keeps
# running ./fridolin from the checkout. build and install are the only
# targets that write ./fridolin, which that service then starts on its next
# restart (scripts/test-makefile.sh holds the other targets to that).
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

# The linters, pinned so a local run and CI report the same findings. The
# Go ones run through `go run`, so nothing needs installing; ruff comes from
# tools/requirements-dev.txt and shellcheck from your package manager.
# Dependabot does not see these two pins, so move them by hand until they
# become tool directives in go.mod, which it does update. staticcheck
# 2026.2.2 was pinned before it was a week old, the wait Dependabot keeps:
# it is the first release that reads Go 1.27.2's export data, and 2026.2.1
# stops with an internal error on the standard library.
STATICCHECK_VERSION := 2026.2.2
ACTIONLINT_VERSION  := v1.7.12
STATICCHECK ?= $(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
ACTIONLINT  ?= $(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
RUFF        ?= ruff
SHELLCHECK  ?= shellcheck

# How long fuzz-short runs each fuzz target.
FUZZTIME ?= 10s

.DEFAULT_GOAL := build
.PHONY: help build install test race vet fmt fmt-check tidy-check \
	lint lint-go lint-sh lint-py lint-actions \
	test-py test-web test-sh smoke fuzz-short check clean

help: ## list the targets
	@awk -F':.*## ' '/^[a-z-]+:.*## / { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## build ./fridolin
	$(GO) build -o $(BINARY) $(MAIN_PKG)

install: build ## copy the binary to BINDIR (default ~/.local/bin)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 $(BINARY) "$(DESTDIR)$(BINDIR)/fridolin"

test: ## Go tests
	$(GO) test -count=1 ./...

race: ## Go tests with the race detector, as CI runs them
	$(GO) test -race -count=1 ./...

# The second vet covers the Linux-only files from macOS (CI also runs Linux).
vet: ## go vet, natively and for Linux without cgo
	$(GO) vet ./...
	CGO_ENABLED=0 GOOS=linux $(GO) vet ./...

fmt: ## gofmt every Go file in place
	gofmt -l -w .

fmt-check: ## fail if a Go file needs gofmt
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "These files need gofmt:"; echo "$$out"; exit 1; fi

tidy-check: ## fail if go.mod or go.sum is not tidy
	$(GO) mod tidy -diff

lint: lint-go lint-sh lint-py lint-actions ## every linter

lint-go: ## staticcheck
	$(STATICCHECK) ./...

lint-sh: ## shellcheck the scripts and git hooks
	$(SHELLCHECK) --severity=warning setup.sh scripts/*.sh .githooks/*

lint-py: ## ruff on tools/ (pip install -r tools/requirements-dev.txt)
	$(RUFF) check tools

lint-actions: ## actionlint the GitHub workflows and check their action pins
	$(ACTIONLINT)
	scripts/check-action-pins.sh

test-py: ## byte-compile the Python tools and run their tests
	$(PYTHON) -m py_compile tools/*.py tools/tests/*.py
	$(PYTHON) -m unittest discover -s tools/tests

# Every dashboard module must parse: one syntax error leaves the whole
# dashboard blank. Unit tests are web/**/*.test.js (or .mjs) for node --test.
# Installed packages (node_modules) are not ours to check.
WEB_FIND = find web -name node_modules -prune -o -type f
test-web: ## syntax-check the dashboard JS and run its unit tests
	@set -e; for f in $$($(WEB_FIND) \( -name '*.js' -o -name '*.mjs' \) -print | sort); do $(NODE) --check "$$f"; done
	@tests="$$($(WEB_FIND) \( -name '*.test.js' -o -name '*.test.mjs' \) -print | sort)"; \
	if [ -n "$$tests" ]; then $(NODE) --test $$tests; else echo "no web unit tests (web/**/*.test.js)"; fi

test-sh: ## tests for the personal-data and action-pin checks and the Makefile
	scripts/test-check-personal-data.sh
	scripts/test-check-action-pins.sh
	scripts/test-makefile.sh

# A temp dir, not ./fridolin: a check must not replace the binary the
# service runs. -race makes a data race at start or shutdown fail the run.
smoke: ## build with -race in a temp dir, start it in dev mode and stop it
	@set -e; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	echo "$(GO) build -race -o $$tmp/fridolin $(MAIN_PKG)"; \
	$(GO) build -race -o "$$tmp/fridolin" $(MAIN_PKG); \
	scripts/smoke.sh "$$tmp/fridolin"

# go test -fuzz takes one target in one package at a time. Listing the
# targets first makes a package that does not compile fail here too.
fuzz-short: ## run every fuzz target for FUZZTIME (default 10s)
	@set -e; for pkg in $$($(GO) list ./...); do \
	  list="$$($(GO) test -list '^Fuzz' "$$pkg")"; \
	  for fn in $$(printf '%s\n' "$$list" | grep '^Fuzz' || true); do \
	    echo "$$pkg $$fn"; \
	    $(GO) test -run '^$$' -fuzz "^$$fn$$" -fuzztime $(FUZZTIME) "$$pkg"; \
	  done; \
	done

# No build: smoke compiles the binary into a temp dir, and ./fridolin, which
# the service runs, stays as it was.
check: fmt-check vet tidy-check race lint test-py test-web test-sh smoke ## what CI's build, lint and test jobs run

clean: ## remove the binary
	rm -f $(BINARY)
