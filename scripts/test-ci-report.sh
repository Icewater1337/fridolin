#!/usr/bin/env bash
#
# Tests for ci-report.sh: each case runs it with a fake gh, which answers
# from files the case wrote (the run's jobs, their logs, the issues, the
# fuzz-failing-inputs artifact) and records what the script asked of it.
#
#   scripts/test-ci-report.sh
#
# Exit status: 0 when every case passed.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
report="$here/ci-report.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
failed=0

mod="github.com/Icewater1337/fridolin"
tip="1111111111111111111111111111111111111111"

# The fake gh. Every call is a line in $FAKE/calls; a --body-file's
# content is kept as $FAKE/body.N, N counting the calls that had one.
cat >"$work/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE/calls"
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
	if [ "${args[i]}" = --body-file ]; then
		n=$(($(ls "$FAKE" | grep -c '^body\.' || true) + 1))
		cp "${args[i + 1]}" "$FAKE/body.$n"
	fi
done
case "$1 $2" in
"api --paginate") cat "$FAKE/jobs.json" ;;
"api repos/"*/logs) id="${2%/logs}"; cat "$FAKE/log.${id##*/}" ;;
"api repos/"*/commits/main) printf '{"sha":"%s"}\n' "$(cat "$FAKE/tip")" ;;
"run download")
	dir="${args[${#args[@]} - 1]}"
	[ -d "$FAKE/artifact" ] || exit 1
	mkdir -p "$dir" && cp -R "$FAKE/artifact/." "$dir/" ;;
"issue list") cat "$FAKE/issues.json" ;;
"issue create") echo "https://github.com/o/r/issues/100" ;;
"label create" | "issue comment" | "issue reopen" | "issue close") ;;
*) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$work/gh"

# setup NAME — a fresh fake for case NAME, with no issues, main at tip.
setup() {
	FAKE="$work/$1"
	mkdir -p "$FAKE"
	: >"$FAKE/calls"
	echo '[]' >"$FAKE/issues.json"
	echo "$tip" >"$FAKE/tip"
}

# jobs ID:CONCLUSION:NAME... — the run's jobs.
jobs() {
	local j out="" id rest conclusion name
	for j in "$@"; do
		id="${j%%:*}" rest="${j#*:}"
		conclusion="${rest%%:*}" name="${rest#*:}"
		out="$out${out:+,}{\"id\":$id,\"conclusion\":\"$conclusion\",\"name\":\"$name\"}"
	done
	printf '{"jobs":[%s,{"id":1,"conclusion":null,"name":"report"}]}\n' "$out" >"$FAKE/jobs.json"
}

# log ID — the log of job ID from stdin, with the timestamps GitHub adds;
# ESC becomes the escape character, which starts the colour codes in one.
esc="$(printf '\033')"
log() {
	sed -e 's/^/2026-10-24T10:41:55.3154436Z /' -e "s/ESC/$esc/g" >"$FAKE/log.$1"
}

run() {
	export FAKE
	GH="$work/gh" GH_TOKEN=x REPO=o/r RUN_ID=42 RUN_ATTEMPT=1 \
		RUN_URL=https://github.com/o/r/actions/runs/42 HEAD_SHA="$tip" SELF_JOB=report \
		GITHUB_STEP_SUMMARY="$FAKE/summary" MAX_ISSUES="${MAX_ISSUES:-10}" \
		"$report" >"$FAKE/out" 2>&1 || echo "exit status $?" >>"$FAKE/out"
}

succeeded() { ! grep -q '^exit status' "$FAKE/out"; }

check() {
	local name="$1"
	shift
	if "$@"; then
		printf 'ok    %s\n' "$name"
	else
		printf 'FAIL  %s\n' "$name"
		sed 's/^/      calls: /' "$FAKE/calls"
		sed 's/^/      out:   /' "$FAKE/out"
		failed=$((failed + 1))
	fi
}

called() { grep -qxF -- "$1" "$FAKE/calls"; }
not_called() { ! grep -qF -- "$1" "$FAKE/calls"; }
body_has() { grep -qF -- "$2" "$FAKE/body.$1"; }
not_in_body() { ! grep -qF -- "$2" "$FAKE/body.$1"; }

# ── a fuzz finding, a test failing on two systems, a build failure, gofmt ──
setup mixed
jobs 10:failure:fuzz "20:failure:go (ubuntu-latest, stable, 1/2)" "21:failure:go (macos-latest, stable, 2/4)" \
	"22:failure:go (ubuntu-latest, stable, 2/2)" 30:success:web
log 10 <<EOF
##[group]FuzzNumberAllowed ($mod/internal/access)
fuzz: elapsed: 0s, gathering baseline coverage: 0/5 completed
--- FAIL: FuzzNumberAllowed (0.21s)
    --- FAIL: FuzzNumberAllowed (0.00s)
        access_fuzz_test.go:31: allowed "+1" but not "1"

    Failing input written to testdata/fuzz/FuzzNumberAllowed/5d5c1c3a9e5f2b7d
    To re-run:
    go test -run=FuzzNumberAllowed/5d5c1c3a9e5f2b7d
FAIL
exit status 1
FAIL	$mod/internal/access	0.236s
##[endgroup]
##[error]fuzzing found a failing input for: FuzzNumberAllowed
EOF
mkdir -p "$FAKE/artifact/internal/access/testdata/fuzz/FuzzNumberAllowed"
printf 'go test fuzz v1\nstring("+1")\n' >"$FAKE/artifact/internal/access/testdata/fuzz/FuzzNumberAllowed/5d5c1c3a9e5f2b7d"
for id in 20 21; do
	log "$id" <<EOF
$mod/internal/app: slice 1 of 2 runs 40 of 80 tests, fuzz targets and examples
--- FAIL: TestStopWaits (10.31s)
    --- FAIL: TestStopWaits/after_kill (10.30s)
        stop_test.go:88: still running after \`\`\`10s\`\`\`
FAIL
FAIL	$mod/internal/app	12.1s
##[error]Process completed with exit code 1.
EOF
done
log 22 <<EOF
# $mod/internal/nodes [$mod/internal/nodes.test]
internal/nodes/join_test.go:12:2: undefined: joinCode
FAIL	$mod/internal/nodes [build failed]
##[error]Process completed with exit code 1.
EOF
cat >"$FAKE/issues.json" <<'EOF'
[{"number":3,"title":"CI on main: FuzzNumberAllowed failing","state":"CLOSED","body":"<!-- ci-main-report kind=fuzz -->"},
 {"number":5,"title":"CI on main: TestStopWaits failing","state":"OPEN","body":"<!-- ci-main-report kind=test -->"},
 {"number":9,"title":"CI on main: TestOld failing","state":"OPEN","body":"<!-- ci-main-report kind=test -->"}]
EOF
run
check "mixed: the script succeeds" succeeded
check "mixed: the fuzz job's artifact is downloaded" called "run download 42 --repo o/r --name fuzz-failing-inputs --dir $(sed -n 's/.*--dir //p' "$FAKE/calls" | head -1)"
check "mixed: a closed issue of the fuzz target is reopened" called "issue reopen 3 --repo o/r"
check "mixed: and gets a comment" grep -q '^issue comment 3 ' "$FAKE/calls"
check "mixed: the open issue of a test gets a comment, once for both systems" test "$(grep -c '^issue comment 5 ' "$FAKE/calls")" = 1
check "mixed: the build failure opens an issue for the package" called "issue create --repo o/r --title CI on main: package internal/nodes failing --label ci-main --body-file $(grep -o 'issue create.*nodes failing.*--body-file [^ ]*' "$FAKE/calls" | sed 's/.*--body-file //')"
check "mixed: an issue whose failure is still red elsewhere stays open" not_called "issue close"
check "mixed: the label exists" grep -q '^label create ci-main ' "$FAKE/calls"
# The bodies, in the order the script went: fuzz, package, test (sorted slugs).
check "mixed: the fuzz body names the target and the package" body_has 1 "**FuzzNumberAllowed** in \`$mod/internal/access\`"
check "mixed: the fuzz body has the failure line" body_has 1 'allowed "+1" but not "1"'
check "mixed: the fuzz body has the input" body_has 1 'string("+1")'
check "mixed: the fuzz body links the run" body_has 1 "[run 42](https://github.com/o/r/actions/runs/42)"
check "mixed: the package body has the compiler's error" body_has 2 "undefined: joinCode"
check "mixed: the test body lists both jobs" body_has 3 '- `go (macos-latest, stable, 2/4)`'
check "mixed: the test body has its subtest" body_has 3 "TestStopWaits/after_kill"
check "mixed: backticks in the log cannot end the code block" body_has 3 '````'
check "mixed: every body carries the marker" grep -q '^<!-- ci-main-report kind=test -->$' "$FAKE/body.3"
check "mixed: no log line reaches the job's own log" not_called "joinCode"
check "mixed: ... nor its output" bash -c "! grep -q 'joinCode\|allowed' '$FAKE/out'"

# ── a job that fails outside the tests ──
setup lint
jobs "20:failure:go (ubuntu-latest, stable, 2/2)" "23:failure:go (macos-latest, stable, 4/4)"
for id in 20 23; do
	log "$id" <<'EOF'
ESC[36;1mRun make fmt-checkESC[0m
internal/app/x.go
##[error]Process completed with exit code 1.
EOF
done
run
check "lint: one issue for the job, whatever it ran on" test "$(grep -c '^issue create --repo o/r --title CI on main: job go failing ' "$FAKE/calls")" = 1
check "lint: the body has the lines before the error" body_has 1 "internal/app/x.go"
check "lint: without the log's colour codes" grep -qx "Run make fmt-check" "$FAKE/body.1"
check "lint: ... or any escape character" not_in_body 1 "$esc"

# ── untrusted names in the log ──
setup hostile
jobs "20:failure:go (ubuntu-latest, stable, 1/2)"
log 20 <<'EOF'
--- FAIL: Test$(touch_pwned) (0.00s)
    x_test.go:1: boom
FAIL	github.com/Icewater1337/fridolin/internal/x	0.1s
--- FAIL: TestReal (0.00s)
    x_test.go:2: boom
FAIL	../../../etc	0.1s
--- FAIL: TestOther (0.00s)
    x_test.go:3: boom
FAIL	example.com/elsewhere	0.1s
##[error]Process completed with exit code 1.
EOF
run
check "hostile: a name that is not a Go identifier gets no issue" not_called 'touch_pwned'
check "hostile: a package path with .. gets no issue" not_called "TestReal"
check "hostile: another module's package gets no issue" not_called "TestOther"
check "hostile: the job still gets one" grep -q '^issue create --repo o/r --title CI on main: job go failing ' "$FAKE/calls"

# ── a green run on main's tip closes what this script opened ──
setup green
jobs 10:success:fuzz "20:success:go (ubuntu-latest, stable, 1/2)" 30:skipped:web
cat >"$FAKE/issues.json" <<'EOF'
[{"number":9,"title":"CI on main: TestOld failing","state":"OPEN","body":"<!-- ci-main-report kind=test -->\nx"},
 {"number":10,"title":"CI on main: FuzzOld failing","state":"OPEN","body":"<!-- ci-main-report kind=fuzz -->\nx"},
 {"number":11,"title":"CI on main: TestMine failing","state":"OPEN","body":"opened by a person"},
 {"number":12,"title":"CI on main: TestGone failing","state":"CLOSED","body":"<!-- ci-main-report kind=test -->"}]
EOF
run
check "green: a test's issue is closed" grep -q '^issue close 9 --repo o/r --reason completed ' "$FAKE/calls"
check "green: a fuzz target's issue is closed" grep -q '^issue close 10 ' "$FAKE/calls"
check "green: an issue a person opened stays open" not_called "issue close 11"
check "green: nothing is opened" not_called "issue create"

# ── the same, but main moved on while the run went ──
setup stale
jobs 10:success:fuzz "20:success:go (ubuntu-latest, stable, 1/2)"
cp "$work/green/issues.json" "$FAKE/issues.json"
echo 2222222222222222222222222222222222222222 >"$FAKE/tip"
run
check "stale: an older run closes nothing" not_called "issue close"

# ── fuzzing passed, a test failed ──
setup partial
jobs 10:success:fuzz "20:failure:go (ubuntu-latest, stable, 1/2)"
log 20 <<EOF
--- FAIL: TestOld (0.00s)
    x_test.go:1: boom
FAIL	$mod/internal/x	0.1s
EOF
cp "$work/green/issues.json" "$FAKE/issues.json"
run
check "partial: the fuzz target's issue is closed" grep -q '^issue close 10 ' "$FAKE/calls"
check "partial: the failing test's issue is not" not_called "issue close 9"
check "partial: it gets a comment" grep -q '^issue comment 9 ' "$FAKE/calls"

# ── more failures than one run reports ──
setup many
jobs "20:failure:go (ubuntu-latest, stable, 1/2)"
log 20 <<EOF
--- FAIL: TestA (0.00s)
    a_test.go:1: boom
--- FAIL: TestB (0.00s)
    b_test.go:1: boom
--- FAIL: TestC (0.00s)
    c_test.go:1: boom
FAIL	$mod/internal/x	0.1s
EOF
MAX_ISSUES=2 run
check "many: two issues of their own" test "$(grep -c '^issue create --repo o/r --title CI on main: Test' "$FAKE/calls")" = 2
check "many: the rest in one more" grep -q '^issue create --repo o/r --title CI on main: more failures than one run reports ' "$FAKE/calls"
check "many: which names it" body_has 3 "CI on main: TestC failing"

if [ "$failed" -gt 0 ]; then
	echo "$failed case(s) failed"
	exit 1
fi
echo "all cases passed"
