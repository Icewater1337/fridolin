#!/bin/sh
# box.sh — make this machine a node of a fridolin core, as a node in a box
# (docs/NODES.md, "Node in a box"): two containers from the images CI
# publishes, with nothing installed on the host but Docker.
#
# The core's !join reply has the line to paste, with this script of the
# core's own version:
#
#   curl -fsSL <source>/container/box.sh | sh -s -- --registry ghcr.io/OWNER \
#     --version VERSION --core fridolin-core.TAILNET.ts.net:8766 --code CODE
#
# It says what it will do and asks before it starts. Then it pulls both
# images and pins them by digest in .env, writes the compose file of that
# version (fridolin box compose), signs the box in to the tailnet as its
# own device (a link to open, or TS_AUTHKEY), joins the core with the code,
# starts the box, and pairs FrIDE with the node: the Claude token for
# FrIDE's agents is typed without echo, never put on a command line.
# Running it again with a new code joins again; what it made stays.
#
# Options:
#   --registry REG     the images' registry, ghcr.io/OWNER (required)
#   --version V        the images' version tag, the core's (required)
#   --core HOST:PORT   the core's node address (required)
#   --code CODE        the join code (else it asks, after the tailnet)
#   --name NAME        how the core shows this node (default: the host name)
#   --backups DIR      a host folder for the backups (default: a volume)
#   --no-fride         a box without FrIDE
#   --dir DIR          where the compose file and .env go (default ~/.fridolin-box)
#   --yes              do not ask before starting
#   --update           update a box made before to --version: pull, pin,
#                      write the compose file, recreate; the node keeps its
#                      key and approval (--core and --code are not needed)
set -eu

registry="" version="" core="" code="" name="" backups="" fride=1 yes=0 update=0
answer="" git_name="" git_email=""
dir="${FRIDOLIN_BOX_DIR:-$HOME/.fridolin-box}"

die() {
	printf 'box.sh: %s\n' "$*" >&2
	exit 1
}
say() { printf '\n==> %s\n' "$*"; }
ask() {
	# ask PROMPT VAR: a line from the terminal, also when the script came
	# through a pipe.
	printf '%s' "$1" >/dev/tty
	IFS= read -r "$2" </dev/tty || true
}

while [ $# -gt 0 ]; do
	case "$1" in
	--registry) registry="$2"; shift 2 ;;
	--version) version="$2"; shift 2 ;;
	--core) core="$2"; shift 2 ;;
	--code) code="$2"; shift 2 ;;
	--name) name="$2"; shift 2 ;;
	--backups) backups="$2"; shift 2 ;;
	--dir) dir="$2"; shift 2 ;;
	--no-fride) fride=0; shift ;;
	--yes) yes=1; shift ;;
	--update) update=1; shift ;;
	-h | --help) sed -n '2,32p' "$0" 2>/dev/null || true; exit 0 ;;
	*) die "unknown option $1 (--help lists them)" ;;
	esac
done
[ -n "$registry" ] || die "--registry is missing (the core's !join reply has the whole line)"
[ -n "$version" ] || die "--version is missing"
[ "$update" = 1 ] || [ -n "$core" ] || die "--core is missing"
case "$registry$version" in *[!A-Za-z0-9._/:-]*) die "--registry or --version has characters an image name cannot" ;; esac
if [ -z "$name" ]; then
	name=$(hostname 2>/dev/null || uname -n)
	name=${name%%.*}
fi
if [ -n "$backups" ]; then
	[ -d "$backups" ] || die "--backups $backups is not a folder"
	backups=$(cd "$backups" && pwd)
fi
node_ref="$registry/fridolin-core:$version"
fride_ref="$registry/fridolin-fride:$version"

if [ "$update" = 1 ]; then
	[ -f "$dir/.env" ] || die "no box in $dir to update (--dir names another folder)"
	grep -q '^COMPOSE_PROFILES=fride' "$dir/.env" || fride=0
	if [ -z "$backups" ]; then
		backups=$(sed -n 's/^FRIDOLIN_BOX_BACKUPS=//p' "$dir/.env")
	fi
	echo "This updates the box in $dir to $version: it pulls the images, pins them, writes the compose file and recreates the services."
else
	cat <<EOF
This makes $name a node of the fridolin core at $core, in a box:

  1. pull $node_ref$( [ "$fride" = 1 ] && printf '\n     and  %s' "$fride_ref")
     and pin both by digest in $dir/.env
  2. write the box's compose file there (docker compose project fridolin-box,
     volumes fridolin-node, $( [ -n "$backups" ] && printf 'backups in %s' "$backups" || printf 'fridolin-backups')$( [ "$fride" = 1 ] && printf ', fride-home, fride-workspaces'))
  3. sign the box in to your tailnet as its own device (a link to open)
  4. join the core with the code from !join, and start the box
EOF
	[ "$fride" = 1 ] && cat <<EOF
  5. FrIDE: sign Claude in for its agents (claude setup-token, in the box),
     set the git identity they commit with, and pair it with the node
EOF
fi
printf '\nNo service gets the Docker socket; FrIDE never sees the node'"'"'s key or backups.\n'
if [ "$yes" != 1 ]; then
	ask "Continue? [y/N] " answer
	case "$answer" in y | Y | yes | YES) ;; *) die "stopped; nothing was changed" ;; esac
fi

say "Checking Docker"
command -v docker >/dev/null 2>&1 || die "docker is not installed (Docker Desktop on macOS and Windows, Docker Engine on Linux)"
docker compose version >/dev/null 2>&1 || die "docker compose is missing (it comes with Docker Desktop; on Linux the docker-compose-plugin package)"
docker info >/dev/null 2>&1 || die "Docker does not answer: start Docker Desktop (or the docker service) and run this again"

say "Pulling the images"
pin() {
	docker pull -q "$1" >/dev/null || die "pulling $1 failed (is the version on the registry, and the package public?)"
	ref=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$1" | grep "^${1%:*}@sha256:" | head -n 1)
	[ -n "$ref" ] || die "no digest for $1"
	printf '%s' "$ref"
}
node_image=$(pin "$node_ref")
echo "node:  $node_image"
fride_image=""
if [ "$fride" = 1 ]; then
	fride_image=$(pin "$fride_ref")
	echo "fride: $fride_image"
fi
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
	repo="${registry#ghcr.io/}/fridolin"
	for img in $node_image $fride_image; do
		gh attestation verify "oci://$img" --repo "$repo" --signer-workflow "$repo/.github/workflows/image.yml" >/dev/null ||
			die "$img has no valid build provenance from $repo; not starting it"
	done
	echo "Build provenance checked with gh."
else
	echo "note: gh is not signed in here, so the images' provenance is not checked now; each later update is checked in the box."
fi

say "Writing $dir"
mkdir -p "$dir"
chmod 700 "$dir"
cd "$dir"
umask 077
{
	echo "# Written by box.sh; box updates rewrite the image lines."
	echo "FRIDOLIN_BOX_NODE_IMAGE=$node_image"
	echo "FRIDOLIN_BOX_FRIDE_IMAGE=${fride_image:-none}"
	[ -n "$backups" ] && echo "FRIDOLIN_BOX_BACKUPS=$backups"
	[ "$fride" = 1 ] && echo "COMPOSE_PROFILES=fride" || echo "COMPOSE_PROFILES="
} >.env
docker run --rm --entrypoint /opt/fridolin/fridolin "$node_image" box compose >compose.yaml.new
mv compose.yaml.new compose.yaml
echo "compose.yaml and .env are in $dir"

if [ "$update" = 1 ]; then
	say "Recreating the box"
	docker compose up -d --remove-orphans
	say "Done: the box runs $version"
	exit 0
fi

say "Signing the box in to the tailnet"
echo "Open the link it prints and sign in with the account of the core's tailnet (or set TS_AUTHKEY before running this)."
docker compose run --rm -T -e TS_AUTHKEY node node tailnet --state-dir /node --name "$name" ||
	die "the box did not get onto the tailnet"

say "Joining the core"
if [ -z "$code" ]; then
	ask "Send !join to fridolin in your WhatsApp chat and type the code it sends: " code
fi
nofride=""
[ "$fride" = 1 ] || nofride="--box-no-fride"
# shellcheck disable=SC2086 # nofride is one flag or none
if ! docker compose run --rm -T node node join "$code" --core "$core" --box --name "$name" --state-dir /node $nofride; then
	die "joining failed. A code works once, within 10 minutes: send !join again, then run this again with --code NEWCODE"
fi
docker compose up -d

if [ "$fride" = 1 ]; then
	say "FrIDE"
	i=0
	until docker compose exec -T fride curl -fsS -o /dev/null http://127.0.0.1:17840/ 2>/dev/null; do
		i=$((i + 1))
		[ "$i" -lt 60 ] || die "FrIDE did not start: docker compose logs fride"
		sleep 2
	done
	restart=0
	if ! docker compose exec -T fride test -s /var/lib/fride/.config/fridolin-box/claude-token; then
		echo "FrIDE's agents need a Claude sign-in. claude setup-token runs in the box now: open the link it prints, sign in, and it shows a token."
		docker compose exec fride claude setup-token </dev/tty >/dev/tty 2>&1 || true
		printf 'Paste the token (it is not shown): ' >/dev/tty
		stty -echo </dev/tty 2>/dev/null || true
		token=""
		IFS= read -r token </dev/tty || true
		stty echo </dev/tty 2>/dev/null || true
		printf '\n' >/dev/tty
		if [ -n "$token" ]; then
			printf '%s\n' "$token" | docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token
			restart=1
		else
			echo "note: no token; later: docker compose exec -T fride /opt/fride/entrypoint.sh set-claude-token, then docker compose restart fride"
		fi
		token=""
	fi
	if [ -z "$(docker compose exec -T fride git config --global user.email 2>/dev/null || true)" ]; then
		ask "The name FrIDE's agents commit with: " git_name
		ask "Their email: " git_email
		[ -n "$git_name" ] && docker compose exec -T fride git config --global user.name "$git_name"
		[ -n "$git_email" ] && docker compose exec -T fride git config --global user.email "$git_email"
	fi
	docker compose exec -T fride fride pair-companion --json |
		docker compose exec -T node fridolin node fride pair --state-dir /node \
			--url "http://127.0.0.1:17840" --webhook "127.0.0.1:17841" --code - ||
		die "pairing FrIDE with the node failed: docker compose logs node fride"
	[ "$restart" = 1 ] && docker compose restart fride >/dev/null
fi

say "Done"
docker compose exec -T node fridolin node status --state-dir /node | sed -n '2,3p'
cat <<EOF

The core tells you in WhatsApp that $name joined; reply !approve <id> there
(or approve it on the dashboard's Nodes page).

In $dir: docker compose ps | logs | stop | start. Remove the box
with docker compose down -v (it deletes the node's key, its backups$( [ -n "$backups" ] && printf ' except %s' "$backups")$( [ "$fride" = 1 ] && printf ' and FrIDE'"'"'s workspaces')).
EOF
