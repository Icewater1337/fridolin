#!/usr/bin/env bash
#
# Reports a CI run on main in issues, so a failure after a merge reaches
# someone (#227). Run by the report job in .github/workflows/ci.yml after
# the other jobs, for pushes to main only.
#
#   scripts/ci-report.sh
#
# It reads the logs of the run's failed jobs and opens one issue per
# failing fuzz target or test, titled "CI on main: NAME failing", with the
# failure, the jobs, the run's link and, for a fuzz target, the failing
# input from the fuzz-failing-inputs artifact. A failed package without a
# failing test (a build failure, a timeout) gets one per package, and a job
# with neither (gofmt, a linter) one per job. An open issue of that title
# gets a comment instead, and a closed one is reopened, so a flaky test
# collects its failures in one place. When the run is on main's current
# commit, an open issue of this script whose failure did not come back is
# closed: a fuzz target's once the fuzz job passed, every other once the
# whole run passed.
#
# Logs are untrusted text: only names that match a strict pattern reach a
# title or the issue's prose, the rest goes into code blocks, and none of it
# is printed to the job's log, where a line could be read as a workflow
# command.
#
# Environment: GH_TOKEN, REPO (owner/name), RUN_ID, RUN_ATTEMPT, RUN_URL,
# HEAD_SHA, SELF_JOB (this job's name, which is still running), and
# optionally GH (the gh to run), MAX_ISSUES (default 10), BRANCH (whose
# newest commit a run must be on to close, default main) and
# GITHUB_STEP_SUMMARY.
#
# Exit status: 0 when everything was reported; 1 when gh failed; 2 for
# usage.

set -euo pipefail

gh="${GH:-gh}"
max="${MAX_ISSUES:-10}"
branch="${BRANCH:-main}"
[[ $max =~ ^[0-9]+$ ]] || { echo "$0: MAX_ISSUES must be a number" >&2; exit 2; }
label="ci-main"
marker="<!-- ci-main-report"
module="github.com/Icewater1337/fridolin"
: "${REPO:?}" "${RUN_ID:?}" "${RUN_ATTEMPT:?}" "${RUN_URL:?}" "${HEAD_SHA:?}" "${SELF_JOB:?}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/logs" "$work/found"

summary() {
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '%s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
	fi
	printf '%s\n' "$*"
}

# fenced FILE — FILE in a code block whose fence is longer than any run of
# backticks inside, so the text cannot end it early.
fenced() {
	local n fence
	n="$(awk '{ s = $0; while (match(s, /`+/)) { if (RLENGTH > m) m = RLENGTH; s = substr(s, RSTART + RLENGTH) } } END { print m + 0 }' "$1")"
	[ "$n" -ge 3 ] || n=2
	fence="$(printf '%*s' $((n + 1)) '' | tr ' ' '`')"
	printf '%s\n' "$fence"
	cat "$1"
	printf '%s\n' "$fence"
}

# parse LOG OUTDIR — one line "kind<TAB>name<TAB>package<TAB>input<TAB>N" per
# failure in a go test log, with its excerpt in OUTDIR/N; the job's error
# lines go to OUTDIR/job either way. kind is test (a --- FAIL at the top
# level; a Fuzz name is a fuzz target) or package (FAIL for a package none
# of whose tests failed: it did not build, panicked or timed out).
parse() {
	awk -v out="$2" '
	function dash(s) { return s == "" ? "-" : s }
	function line(s) { return length(s) > 300 ? substr(s, 1, 300) "…" : s }
	function add(kind, nm, inp, t) { n++; ispkg[n] = kind; name[n] = nm; input[n] = inp; text[n] = t; pending[n] = 1 }
	function finish() { if (cur != "") add(0, cur, in_path, buf); cur = "" }
	{
		sub(/\r$/, "")
		gsub(/\033\[[0-9;?]*[A-Za-z]/, "")
		gsub(/[\001-\010\013-\037\177]/, "")
		sub(/^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9:.]*Z /, "")
		ring[NR % 20] = $0
	}
	/##\[error\]/ {
		if (errs == 0) for (i = NR - 19; i < NR; i++) if (i > 0) joberr = joberr line(ring[i % 20]) "\n"
		if (errs++ < 20) joberr = joberr line($0) "\n"
	}
	cur != "" {
		if ($0 ~ /^[ \t]/ || $0 == "") {
			if (lines++ < 40) buf = buf line($0) "\n"
			if (match($0, /Failing input written to testdata\/fuzz\/[^ ]+/)) in_path = substr($0, RSTART + 25, RLENGTH - 25)
			next
		}
		finish()
	}
	/^--- FAIL: / { cur = $3; buf = line($0) "\n"; lines = 1; in_path = ""; tests++; next }
	/^panic: / { panic = ""; panic_lines = 12 }
	panic_lines > 0 { panic = panic line($0) "\n"; panic_lines-- }
	/^FAIL\t/ {
		finish()
		split($0, parts, "\t"); pkg = parts[2]; sub(/ .*/, "", pkg)
		if (tests == 0) {
			t = panic
			if (t == "") for (i = NR - 19; i <= NR; i++) if (i > 0) t = t line(ring[i % 20]) "\n"
			add(1, pkg, "", t)
		} else if (panic != "") {
			# A test that panics ends its package: the panic says why.
			for (k in pending) text[k] = text[k] "\n" panic
		}
		for (k in pending) { package[k] = pkg; delete pending[k] }
		tests = 0; panic = ""
	}
	END {
		finish()
		f = out "/job"; printf "%s", joberr > f; close(f)
		for (k = 1; k <= n; k++) {
			f = out "/" k; printf "%s", text[k] > f; close(f)
			# "-" for an empty field: read would run two tabs together.
			printf "%s\t%s\t%s\t%s\t%s\n", (ispkg[k] ? "package" : "test"), name[k], dash(package[k]), dash(input[k]), k
		}
	}' "$1"
}

# record SLUG TITLE KIND JOB EXCERPT [PACKAGE [INPUT]] — adds a failure of
# this run to the issue titled TITLE. The first job's excerpt is kept.
record() {
	local d="$work/found/$1"
	if [ ! -d "$d" ]; then
		mkdir -p "$d"
		printf '%s\n' "$2" >"$d/title"
		printf '%s\n' "$3" >"$d/kind"
		cp "$5" "$d/excerpt"
		printf '%s\n' "${6:-}" >"$d/package"
		printf '%s\n' "${7:-}" >"$d/input"
	fi
	grep -qxF -- "$4" "$d/jobs" 2>/dev/null || printf '%s\n' "$4" >>"$d/jobs"
}

"$gh" api --paginate "repos/$REPO/actions/runs/$RUN_ID/attempts/$RUN_ATTEMPT/jobs" |
	jq -r --arg self "$SELF_JOB" '.jobs[] | select(.name != $self and .conclusion != null) | [.id, .conclusion, .name] | @tsv' >"$work/jobs"

fuzz_ok=0 all_ok=1
while IFS=$'\t' read -r id conclusion job; do
	[ "$job" = fuzz ] && [ "$conclusion" = success ] && fuzz_ok=1
	case "$conclusion" in
	success | skipped) continue ;;
	esac
	all_ok=0
	[ "$conclusion" = failure ] || continue
	[[ $id =~ ^[0-9]+$ ]] || continue
	# The jobs' names come from this workflow; keep to what one looks like.
	[[ $job =~ ^[A-Za-z0-9\ ._,/()-]+$ ]] || job="job $id"
	[ "$job" != fuzz ] || [ -d "$work/inputs" ] ||
		"$gh" run download "$RUN_ID" --repo "$REPO" --name fuzz-failing-inputs --dir "$work/inputs" </dev/null >/dev/null 2>&1 ||
		mkdir -p "$work/inputs"
	log="$work/logs/$id"
	mkdir -p "$log.d"
	# Logs have colour codes, which gh refuses to print without the flag;
	# parse takes them out, with every other control character.
	"$gh" api "repos/$REPO/actions/jobs/$id/logs" --allow-escape-sequences </dev/null >"$log"
	parse "$log" "$log.d" >"$log.tsv"
	reported=0
	while IFS=$'\t' read -r kind name pkg input n; do
		# Only this module's packages: the path also finds a fuzz input.
		[[ $pkg =~ ^[A-Za-z0-9._/-]+$ && $pkg != *..* ]] || continue
		[[ $pkg == "$module" || $pkg == "$module"/* ]] || continue
		short="${pkg#"$module"/}"
		[ "$short" != "$module" ] || short="."
		if [ "$kind" = package ]; then
			record "package-${short//\//-}" "CI on main: package $short failing" package "$job" "$log.d/$n" "$pkg"
			reported=1
			continue
		fi
		# A subtest is reported with its test.
		name="${name%%/*}"
		[[ $name =~ ^(Test|Fuzz)[A-Za-z0-9_]*$ ]] || continue
		[ "$input" != - ] || input=""
		[[ $input =~ ^testdata/fuzz/Fuzz[A-Za-z0-9_]*/[0-9a-f]+$ ]] || input=""
		case "$name" in
		Fuzz*) record "fuzz-$name" "CI on main: $name failing" fuzz "$job" "$log.d/$n" "$pkg" "$short/$input" ;;
		*) record "test-$name" "CI on main: $name failing" test "$job" "$log.d/$n" "$pkg" ;;
		esac
		reported=1
	done <"$log.tsv"
	if [ "$reported" = 0 ]; then
		# Not a test: one issue per job, whatever system or release it ran on.
		base="${job%% (*}"
		[ -s "$log.d/job" ] || printf '(no error lines in the log)\n' >"$log.d/job"
		record "job-${base//[^A-Za-z0-9_-]/-}" "CI on main: job $base failing" job "$job" "$log.d/job"
	fi
done <"$work/jobs"

"$gh" issue list --repo "$REPO" --label "$label" --state all --limit 500 --json number,title,state,body >"$work/issues"

# body DIR — what an issue, or a comment on one, says about this failure.
body() {
	local d="$1" kind input file
	kind="$(cat "$d/kind")"
	input="$(cat "$d/input")"
	printf '%s kind=%s -->\n' "$marker" "$kind"
	case "$kind" in
	fuzz | test) printf '**%s** in `%s` failed on `main` at %s in [run %s](%s).\n\n' "$(cut -d' ' -f4 "$d/title")" "$(cat "$d/package")" "$HEAD_SHA" "$RUN_ID" "$RUN_URL" ;;
	package) printf 'Package `%s` failed on `main` at %s in [run %s](%s), without a failing test: it did not build, panicked or timed out.\n\n' "$(cat "$d/package")" "$HEAD_SHA" "$RUN_ID" "$RUN_URL" ;;
	job) printf 'A job failed on `main` at %s in [run %s](%s), outside the tests.\n\n' "$HEAD_SHA" "$RUN_ID" "$RUN_URL" ;;
	esac
	printf 'Jobs:\n'
	sed 's/.*/- `&`/' "$d/jobs"
	printf '\nFailure (from the first job):\n\n'
	fenced "$d/excerpt"
	if [ "$kind" = fuzz ]; then
		file="$work/inputs/$input"
		if [ -n "$input" ] && [ -f "$file" ] && [ ! -L "$file" ]; then
			head -c 4000 "$file" >"$d/input-text"
			printf '\nThe failing input, `%s` in the run'"'"'s `fuzz-failing-inputs` artifact:\n\n' "$input"
			fenced "$d/input-text"
			printf '\nCommit it there with the fix; `go test` runs it from then on.\n'
		else
			printf '\nThe failing input was not found in the run'"'"'s `fuzz-failing-inputs` artifact.\n'
		fi
	fi
	printf '\nOpened and updated by the report job of `.github/workflows/ci.yml`, which closes it once a later run on `main` passes.\n'
}

if [ -n "$(ls "$work/found")" ]; then
	"$gh" label create "$label" --repo "$REPO" --force --color B60205 --description "A CI failure on main, reported by the report job" >/dev/null
fi
count=0 overflow=""
reported_titles="$work/reported"
: >"$reported_titles"
for d in "$work/found"/*; do
	[ -d "$d" ] || continue
	title="$(cat "$d/title")"
	printf '%s\n' "$title" >>"$reported_titles"
	if [ "$count" -ge "$max" ]; then
		overflow="$overflow$title"$'\n'
		continue
	fi
	count=$((count + 1))
	body "$d" >"$d/body"
	existing="$(jq -r --arg t "$title" '[.[] | select(.title == $t)] | (map(select(.state == "OPEN")) + (map(select(.state != "OPEN")) | sort_by(-.number))) | .[0].number // empty' "$work/issues")"
	if [ -z "$existing" ]; then
		url="$("$gh" issue create --repo "$REPO" --title "$title" --label "$label" --body-file "$d/body")"
		summary "opened: $title ($url)"
	else
		state="$(jq -r --argjson n "$existing" '.[] | select(.number == $n) | .state' "$work/issues")"
		if [ "$state" != OPEN ]; then
			"$gh" issue reopen "$existing" --repo "$REPO" >/dev/null
		fi
		"$gh" issue comment "$existing" --repo "$REPO" --body-file "$d/body" >/dev/null
		summary "updated: $title (#$existing)"
	fi
done

if [ -n "$overflow" ]; then
	title="CI on main: more failures than one run reports"
	printf '%s\n' "$title" >>"$reported_titles"
	{
		printf '%s kind=overflow -->\n' "$marker"
		printf '[Run %s](%s) at %s had more than %s failures. These got no issue of their own:\n\n' "$RUN_ID" "$RUN_URL" "$HEAD_SHA" "$max"
		printf '%s' "$overflow" | sed 's/.*/- &/'
		printf '\nOpened and updated by the report job of `.github/workflows/ci.yml`, which closes it once a later run on `main` passes.\n'
	} >"$work/overflow"
	existing="$(jq -r --arg t "$title" '[.[] | select(.title == $t and .state == "OPEN")] | .[0].number // empty' "$work/issues")"
	if [ -z "$existing" ]; then
		"$gh" issue create --repo "$REPO" --title "$title" --label "$label" --body-file "$work/overflow" >/dev/null
	else
		"$gh" issue comment "$existing" --repo "$REPO" --body-file "$work/overflow" >/dev/null
	fi
	summary "$(printf '%s' "$overflow" | wc -l | tr -d ' ') more failures listed in: $title"
fi

# Closing needs this run to be on main's newest commit: an older run that
# finishes late says nothing about main as it is.
tip="$("$gh" api "repos/$REPO/commits/$branch" | jq -r .sha)"
if [ "$tip" != "$HEAD_SHA" ]; then
	summary "$branch moved on to $tip: no issue closed"
	exit 0
fi
jq -r --arg m "$marker kind=" '.[] | select(.state == "OPEN" and (.body | startswith($m))) | [.number, (.body | ltrimstr($m) | split(" ")[0]), .title] | @tsv' "$work/issues" |
	while IFS=$'\t' read -r number kind title; do
		grep -qxF -- "$title" "$reported_titles" && continue
		if [ "$all_ok" = 1 ] || { [ "$kind" = fuzz ] && [ "$fuzz_ok" = 1 ]; }; then
			"$gh" issue close "$number" --repo "$REPO" --reason completed \
				--comment "Passed on \`main\` at $HEAD_SHA in [run $RUN_ID]($RUN_URL). Reopened if it fails again." >/dev/null
			summary "closed: #$number"
		fi
	done
