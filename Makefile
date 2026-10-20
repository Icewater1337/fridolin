# Build, test and lint Fridolin. CI's build, lint and test jobs run these
# same targets, so a passing `make check` here should mean those pass in CI
# too (on this OS). CI also runs the site's tests (site/, which need its
# npm packages), the fuzz targets on pushes to main (fuzz-short runs them
# here), govulncheck, CodeQL and the personal-data scan of the pushed
# commits; make check does not.
#
#   make build        bin/fridolin
#   make check        what CI's build, lint and test jobs run
#   make help         every target

# The main package. The repository's root has no Go files (root-check).
MAIN_PKG := ./cmd/fridolin
BINARY   ?= bin/fridolin
# The version `fridolin version` and the log's first line report; without
# git (a checkout from an archive) it is "dev".
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Lazy (=), so only a target that builds runs git describe.
# The variable is version in internal/version.
VERSION_LDFLAGS = -X github.com/Icewater1337/fridolin/internal/version.version=$(VERSION)

GO     ?= go
PYTHON ?= python3
NODE   ?= node

# make install copies the binary here, to run it by hand. The service
# setup.sh installs runs its own copy in ~/.local/lib/fridolin (`fridolin
# service install`), which no target touches. Builds go to bin/, not
# ./fridolin, which a service set up by an older setup.sh still starts on
# its next restart; build and install are the only targets that write the
# binary at all (scripts/test-makefile.sh holds the other targets to that).
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

# Go linters are pinned: staticcheck is a go.mod tool so Dependabot sees it;
# actionlint remains an explicit version here. ruff comes from
# tools/requirements-dev.txt and shellcheck from your package manager.
ACTIONLINT_VERSION  := v1.7.12
STATICCHECK ?= $(GO) tool staticcheck
ACTIONLINT  ?= $(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
RUFF        ?= ruff
SHELLCHECK  ?= shellcheck

# How long fuzz-short runs each fuzz target.
FUZZTIME ?= 10s

.DEFAULT_GOAL := build
.PHONY: help build install test race race-shard race-process vet fmt fmt-check tidy-check root-check \
	notices notices-check \
	lint lint-go lint-sh lint-py lint-actions lint-web \
	test-py test-web test-sh smoke fuzz-short check clean

help: ## list the targets
	@awk -F':.*## ' '/^[a-z-]+:.*## / { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## build bin/fridolin, stamped with VERSION (default: git describe)
	$(GO) build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o "$(BINARY)" $(MAIN_PKG)

install: build ## copy the binary to BINDIR (default ~/.local/bin)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(DESTDIR)$(BINDIR)/fridolin"

test: ## Go tests
	$(GO) test -count=1 ./...

race: ## Go tests with the race detector, as CI runs them
	$(GO) test -race -count=1 ./...

race-shard: ## one slice of the race tests, SHARD=i/n (CI splits the macOS run)
	GO="$(GO)" scripts/test-shard.sh $(subst /, ,$(SHARD)) -race -count=1

race-process: ## repeat extracted provider process tests under the race detector
	$(GO) test -race -shuffle=on -count=5 ./internal/claudecli

# The other vets cover the Linux- and Windows-only files from macOS (CI also
# runs Linux and Windows).
vet: ## go vet, natively and for Linux and Windows without cgo
	$(GO) vet ./...
	CGO_ENABLED=0 GOOS=linux $(GO) vet ./...
	CGO_ENABLED=0 GOOS=windows $(GO) vet ./...

fmt: ## gofmt every Go file in place
	gofmt -l -w .

fmt-check: ## fail if a Go file needs gofmt
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "These files need gofmt:"; echo "$$out"; exit 1; fi

tidy-check: ## fail if go.mod or go.sum is not tidy
	$(GO) mod tidy -diff

# THIRD_PARTY_NOTICES lists the modules the binary links and their
# licences, which release archives ship next to LICENSE. Run notices after
# changing dependencies: notices-check fails while the file is out of date.
notices: ## write THIRD_PARTY_NOTICES from the modules the binary links
	GO="$(GO)" scripts/third-party-notices.sh

notices-check: ## fail if THIRD_PARTY_NOTICES is out of date
	GO="$(GO)" scripts/third-party-notices.sh --check

# With a Go file at the root, `go build -o fridolin .` there would build
# (a non-main package into an ar archive) over the ./fridolin a service
# set up by an older setup.sh still runs. With none it must fail, and with
# "no Go files". -o /dev/null: should it build, nothing is written.
root-check: ## fail unless go build at the repo root refuses to build
	@if out="$$($(GO) build -o /dev/null . 2>&1)"; then \
	  echo "go build . at the repository's root built a package: Go code belongs in cmd/fridolin or internal/"; exit 1; \
	fi; \
	case "$$out" in *"no Go files"*) ;; *) printf 'go build . at the root failed, but not for want of Go files:\n%s\n' "$$out"; exit 1 ;; esac

lint: lint-go lint-sh lint-py lint-actions lint-web ## every linter

lint-go: ## staticcheck
	$(STATICCHECK) ./...

lint-sh: ## shellcheck the scripts and git hooks
	$(SHELLCHECK) --severity=warning setup.sh scripts/*.sh container/*.sh .githooks/*

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

# ESLint on the dashboard's JS, with web/eslint.config.js. npx fetches the
# pinned version into its own cache the first time (that needs the
# network), so nothing is installed into the checkout and a local run and
# CI run the same ESLint. Only ESLint itself is pinned: npm resolves
# its dependencies afresh for each new cache, within the ranges ESLint
# allows, and runs none of their install scripts. Dependabot does not see
# this pin either, so move it by hand; 10.12.0 was two weeks old when it
# was pinned. It needs Node 20.19+, 22.13+ or 24+.
ESLINT_VERSION := 10.12.0
NPX    ?= npx
ESLINT ?= $(NPX) --yes eslint@$(ESLINT_VERSION)
lint-web: ## ESLint on the dashboard's JS (npx fetches it)
	cd web && NPM_CONFIG_IGNORE_SCRIPTS=true $(ESLINT) .

test-sh: ## tests for the personal-data, action-pin, notices and release scripts and the Makefile
	scripts/test-check-personal-data.sh
	scripts/test-check-action-pins.sh
	scripts/test-third-party-notices.sh
	scripts/test-build-release.sh
	scripts/test-makefile.sh
	scripts/test-test-shard.sh

# A temp dir, not the binary: a check must not replace a build in use.
# -race makes a data race at start or shutdown fail the run. The build is
# stamped as make build stamps it, and smoke.sh checks that fridolin
# version prints the stamp: the linker skips an -X whose variable does not
# exist, so a stamp that stopped working would go unnoticed otherwise.
smoke: ## build with -race in a temp dir, start it in dev mode and stop it
	@set -e; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	echo "$(GO) build -race -ldflags \"$(VERSION_LDFLAGS)\" -o $$tmp/fridolin $(MAIN_PKG)"; \
	$(GO) build -race -ldflags "$(VERSION_LDFLAGS)" -o "$$tmp/fridolin" $(MAIN_PKG); \
	scripts/smoke.sh "$$tmp/fridolin" "$(VERSION)"

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

# No build: smoke compiles the binary into a temp dir, and bin/fridolin stays
# as it was.
check: fmt-check vet tidy-check notices-check root-check race race-process lint test-py test-web test-sh smoke ## what CI's build, lint and test jobs run

clean: ## remove the binary
	rm -f "$(BINARY)"
