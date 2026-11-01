# Build, test and lint Fridolin. CI's build, lint and test jobs run these
# same targets, so a passing `make check` here should mean those pass in CI
# too (on this OS). CI's fuzz job runs fuzz-short, for 20 seconds a target,
# on pushes to main. CI also runs the site's tests (site/, which need its
# npm packages), govulncheck, CodeQL and the personal-data scan of the
# pushed commits; make check runs none of these, nor fuzz-short.
#
#   make build        bin/fridolin
#   make check        what CI's build, lint and test jobs run
#   make help         every target

# The main package. The repository's root has no Go files (root-check).
MAIN_PKG := ./cmd/fridolin
BINARY   ?= bin/fridolin
# The version `fridolin version` and the log's first line report; without
# git (a checkout from an archive) it is "dev".
VERSION  ?= $(shell git describe --tags --always --dirty --abbrev=8 2>/dev/null || echo dev)
# Lazy (=), so only a target that builds runs git describe.
# The variable is version in internal/version.
VERSION_LDFLAGS = -X github.com/Icewater1337/fridolin/internal/version.version=$(VERSION)
# A built-in Google OAuth client (internal/google/builtin.go), for a
# publisher's builds: the shell expands FRIDOLIN_GOOGLE_CLIENT_ID and
# FRIDOLIN_GOOGLE_CLIENT_SECRET from the environment, so the line make
# prints names them without their values. Unset, the build has none and
# its command line stays as it was.
GOOGLE_LDFLAGS = $(if $(FRIDOLIN_GOOGLE_CLIENT_ID),-X github.com/Icewater1337/fridolin/internal/google.builtinClientID=$${FRIDOLIN_GOOGLE_CLIENT_ID} -X github.com/Icewater1337/fridolin/internal/google.builtinClientSecret=$${FRIDOLIN_GOOGLE_CLIENT_SECRET:-} )

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

# Go linters are pinned: staticcheck and exhaustive are go.mod tools;
# actionlint is an explicit version here, which Renovate moves through
# the "renovate:" comment above it (.github/renovate.json5). ruff
# comes from tools/requirements-dev.txt and shellcheck from your package
# manager.
# renovate: datasource=github-releases depName=rhysd/actionlint
ACTIONLINT_VERSION  := 1.7.12
STATICCHECK ?= $(GO) tool staticcheck
EXHAUSTIVE  ?= $(GO) tool exhaustive
ACTIONLINT  ?= $(GO) run github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION)
RUFF        ?= ruff
SHELLCHECK  ?= shellcheck

# How long fuzz-short runs each fuzz target, and how long the engine may
# spend shrinking a failing input it found (60 seconds by default, longer
# than a target's run).
FUZZTIME         ?= 10s
FUZZMINIMIZETIME ?= 5s

.DEFAULT_GOAL := build
.PHONY: help build install node-binaries test race race-shard race-process os-tests vet fmt fmt-check tidy-check root-check \
	notices notices-check \
	lint lint-go lint-go-os lint-sh lint-py lint-actions lint-web typecheck-web \
	test-py test-web test-sh smoke fuzz-short check clean container-python-lock

help: ## list the targets
	@awk -F':.*## ' '/^[a-z-]+:.*## / { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: ## build bin/fridolin, stamped with VERSION (default: git describe)
	$(GO) build -trimpath -ldflags "$(GOOGLE_LDFLAGS)$(VERSION_LDFLAGS)" -o "$(BINARY)" $(MAIN_PKG)

install: build ## copy the binary to BINDIR (default ~/.local/bin)
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(DESTDIR)$(BINDIR)/fridolin"

# Where node-binaries writes; the service's install finds them in
# ~/.local/lib/fridolin/node-binaries (or $$FRIDOLIN_NODE_BINARIES).
NODE_BINARIES ?= bin/node-binaries

node-binaries: ## the builds a core updates its nodes to, for each platform, into NODE_BINARIES
	GO="$(GO)" scripts/build-node-binaries.sh "$(VERSION)" "$(NODE_BINARIES)"

test: ## Go tests
	$(GO) test -count=1 ./...

race: ## Go tests with the race detector, as CI runs them
	$(GO) test -race -count=1 -timeout 30m ./...

race-shard: ## one slice of the race tests, SHARD=i/n (CI splits the macOS run)
	GO="$(GO)" scripts/test-shard.sh $(subst /, ,$(SHARD)) -race -count=1 -timeout 30m

# macOS and Windows CI run only these; Linux runs everything, and the full
# macOS suite runs nightly (.github/workflows/macos-full.yml).
os-tests: ## race tests of what differs on this system from Linux (scripts/os-tests.sh)
	GO="$(GO)" scripts/os-tests.sh -race -count=1 -timeout 30m

race-process: ## repeat extracted provider process tests under the race detector
	$(GO) test -race -shuffle=on -count=5 -timeout 30m ./internal/claudecli

# The other vets cover the files of the other systems from any one of them,
# so CI vets all three on Linux.
vet: ## go vet, natively and for Linux, macOS and Windows without cgo
	$(GO) vet ./...
	CGO_ENABLED=0 GOOS=linux $(GO) vet ./...
	CGO_ENABLED=0 GOOS=darwin $(GO) vet ./...
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

lint: lint-go lint-go-os lint-sh lint-py lint-actions lint-web typecheck-web ## every linter

# exhaustive holds a switch on an enum (a defined type with constants
# declared beside it) to a case for every constant, a default or not, so
# that a new constant fails here at every switch that has to decide about
# it. A default stays for values from outside (a string from JSON, a
# number out of range), and a switch meant to cover only some constants
# says why in a comment above its //exhaustive:ignore. A switch on
# reflect.Kind picks a few of its 27 kinds by nature, so none is checked.
lint-go: ## staticcheck, and exhaustive on switches over enums
	$(STATICCHECK) ./...
	$(EXHAUSTIVE) -typeignore '^reflect\.Kind$$' ./...

# The same linters on another system's files, from any machine: they are
# built for this one and analyse for LINT_GOOS (go tool would build them
# for LINT_GOOS). CI lints macOS's files this way on Linux.
LINT_GOOS ?= darwin
lint-go-os: ## staticcheck and exhaustive for LINT_GOOS (default darwin)
	@set -e; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	$(GO) build -o "$$tmp/" honnef.co/go/tools/cmd/staticcheck github.com/nishanths/exhaustive/cmd/exhaustive; \
	echo "GOOS=$(LINT_GOOS) staticcheck ./... && exhaustive ./..."; \
	CGO_ENABLED=0 GOOS=$(LINT_GOOS) "$$tmp/staticcheck" ./...; \
	CGO_ENABLED=0 GOOS=$(LINT_GOOS) "$$tmp/exhaustive" -typeignore '^reflect\.Kind$$' ./...

lint-sh: ## shellcheck the scripts and git hooks
	$(SHELLCHECK) --severity=warning setup.sh scripts/*.sh container/*.sh container/fride/*.sh .githooks/*

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
# allows, and runs none of their install scripts. Renovate moves the pin
# through the comment above it. It needs Node 20.19+, 22.13+ or 24+.
# renovate: datasource=npm depName=eslint
ESLINT_VERSION := 10.13.0
NPX    ?= npx
ESLINT ?= $(NPX) --yes eslint@$(ESLINT_VERSION)
lint-web: ## ESLint on the dashboard's JS (npx fetches it)
	cd web && NPM_CONFIG_IGNORE_SCRIPTS=true $(ESLINT) .

# TypeScript's checker on the dashboard's JS and its JSDoc types, with
# nothing built: web/tsconfig.json checks the modules against the
# browser's DOM, web/test/tsconfig.json the tests (with node's types)
# against the fake DOM they run in. npx fetches the pinned versions as it
# does ESLint, so nothing is installed into the checkout. TypeScript 7 is
# the native compiler, whose only dependencies are its binaries, one per
# platform, at the same version. With no node_modules in web/, tsc finds
# @types/node in npx's cache through --typeRoots. Renovate moves the pins
# through the comments above them.
# renovate: datasource=npm depName=typescript
TYPESCRIPT_VERSION := 7.0.2
# renovate: datasource=npm depName=@types/node
TYPES_NODE_VERSION := 24.19.2
TSC_NPX ?= $(NPX) --yes -p typescript@$(TYPESCRIPT_VERSION) -p @types/node@$(TYPES_NODE_VERSION)
typecheck-web: ## tsc --noEmit on the dashboard's JS and tests (npx fetches it)
	cd web && NPM_CONFIG_IGNORE_SCRIPTS=true $(TSC_NPX) -c 'tsc -p . && tsc -p test --typeRoots "$$(dirname "$$(command -v tsc)")/../@types"'

test-sh: ## tests for the personal-data, action-pin, notices, release, OS-test and CI report scripts, FrIDE's token store and the Makefile
	scripts/test-check-personal-data.sh
	scripts/test-check-action-pins.sh
	scripts/test-third-party-notices.sh
	scripts/test-build-release.sh
	scripts/test-makefile.sh
	scripts/test-test-shard.sh
	scripts/test-os-tests.sh
	scripts/test-ci-report.sh
	scripts/test-fride-entrypoint.sh

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

# go test -fuzz takes one target in one package at a time, so each target
# in turn, found by name: a new one needs no change here. Listing the
# targets first makes a package that does not compile fail here too. A
# target that fails does not stop the others; the run fails at the end and
# names them. The tests link testdata in their working directory to the
# package's (LinkTestdata in internal/testutil), and the engine saves a
# failing input there: without the folder the link dangles, and the input
# would only be in the log. In GitHub Actions each target is a group of
# the log, and the failing ones an error.
fuzz-short: ## run every fuzz target for FUZZTIME (default 10s)
	@set -e; gha="$${GITHUB_ACTIONS:-}"; failed=""; \
	for pkg in $$($(GO) list ./...); do \
	  list="$$($(GO) test -list '^Fuzz' "$$pkg")"; \
	  targets="$$(printf '%s\n' "$$list" | grep '^Fuzz' || true)"; \
	  [ -n "$$targets" ] || continue; \
	  mkdir -p "$$($(GO) list -f '{{.Dir}}' "$$pkg")/testdata"; \
	  for fn in $$targets; do \
	    if [ -n "$$gha" ]; then echo "::group::$$fn ($$pkg)"; else echo "$$pkg $$fn"; fi; \
	    $(GO) test -run '^$$' -fuzz "^$$fn$$" -fuzztime $(FUZZTIME) -fuzzminimizetime $(FUZZMINIMIZETIME) "$$pkg" || \
	      failed="$$failed $$fn"; \
	    if [ -n "$$gha" ]; then echo "::endgroup::"; fi; \
	  done; \
	done; \
	if [ -n "$$failed" ]; then \
	  if [ -n "$$gha" ]; then echo "::error::fuzzing found a failing input for:$$failed"; \
	  else echo "fuzzing found a failing input for:$$failed" >&2; fi; \
	  exit 1; \
	fi

# The image's Python packages (container/python), pinned with hashes for
# linux/arm64 and linux/amd64 alike: torch from the PyTorch CPU index, and
# nothing newer than PYLOCK_BEFORE (releases at least two weeks old).
UV ?= uv
PYLOCK_BEFORE ?= $(shell date -u -v-14d +%Y-%m-%d 2>/dev/null || date -u -d '14 days ago' +%Y-%m-%d)
PYLOCK_ARGS = requirements.in --python-version 3.11 --generate-hashes --no-header --no-annotate -q \
	--index-url https://pypi.org/simple --extra-index-url https://download.pytorch.org/whl/cpu \
	--index-strategy unsafe-best-match --exclude-newer $(PYLOCK_BEFORE)
container-python-lock: ## pin container/python/requirements.in into requirements.lock (needs uv and the network)
	@set -e; cd container/python; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	$(UV) pip compile $(PYLOCK_ARGS) --python-platform aarch64-manylinux_2_28 >"$$tmp/arm64"; \
	$(UV) pip compile $(PYLOCK_ARGS) --python-platform x86_64-manylinux_2_28 >"$$tmp/amd64"; \
	cmp -s "$$tmp/arm64" "$$tmp/amd64" || { echo "arm64 and amd64 resolve differently" >&2; exit 1; }; \
	mv "$$tmp/arm64" requirements.lock

# No build: smoke compiles the binary into a temp dir, and bin/fridolin stays
# as it was.
check: fmt-check vet tidy-check notices-check root-check race race-process lint test-py test-web test-sh smoke ## what CI's build, lint and test jobs run

clean: ## remove the binary
	rm -f "$(BINARY)"
