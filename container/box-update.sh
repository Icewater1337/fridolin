#!/bin/sh
# The box's updater (docs/NODES.md, "Box updates"). box.sh writes it
# beside the box's compose file (fridolin box updater sh prints it) and
# runs it every hour as the owner: a launchd agent on macOS, a
# systemd user timer on Linux, else cron.
#
# The box's services never reach Docker; this does, on the host, for one
# thing only. Each round it asks a throwaway container of the image the
# box runs now whether the core asked for a version: that container
# checks the request's signature against the core's key the box pinned,
# that both images are of the box's own registry, and their build
# provenance (fridolin box pending). Then it pulls them by digest, writes
# .env and the new version's compose files (compose.yaml, and core.yaml
# with seccomp.json: the core's settings, which a box made before the core
# could run in a box gets here), recreates the services and
# waits up to 5 minutes for the node to be connected to the core again.
# If it is not, the .env and compose file of before come back. The result
# goes into the node's volume, and the agent reports it to the core.
set -eu
cd "$(dirname "$0")"
PATH="$PATH:/usr/local/bin:/opt/homebrew/bin:$HOME/.orbstack/bin:/Applications/Docker.app/Contents/Resources/bin"
volume=fridolin-node

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>update.log; }

# One round at a time; a lock left by a round that died goes after an hour.
lock=.update.lock
if ! mkdir "$lock" 2>/dev/null; then
	[ -n "$(find "$lock" -maxdepth 0 -mmin +60 2>/dev/null)" ] || exit 0
	rmdir "$lock" 2>/dev/null || exit 0
	mkdir "$lock" 2>/dev/null || exit 0
fi
trap 'rmdir "$lock" 2>/dev/null || true' EXIT

docker info >/dev/null 2>&1 || exit 0
current=$(sed -n 's/^FRIDOLIN_BOX_NODE_IMAGE=//p' .env)
[ -n "$current" ] || exit 0
# Until docker compose made the node's volume (box.sh installs the updater
# first), there is nothing to update; a docker run -v here would make the
# volume outside compose, which then never removes it with down -v.
docker volume inspect "$volume" >/dev/null 2>&1 || exit 0

set +e
answer=$(docker run --rm -v "$volume:/node" --entrypoint /opt/fridolin/fridolin "$current" box pending --state-dir /node 2>>update.log)
status=$?
set -e
[ "$status" = 3 ] && exit 0
if [ "$status" != 0 ]; then
	log "the core's request was refused (above); nothing changed"
	exit 0
fi
version=$(printf '%s\n' "$answer" | sed -n 's/^VERSION=//p')
node=$(printf '%s\n' "$answer" | sed -n 's/^NODE_IMAGE=//p')
fride=$(printf '%s\n' "$answer" | sed -n 's/^FRIDE_IMAGE=//p')
for v in "$version" "$node" "$fride"; do
	case "$v" in "" | *[!A-Za-z0-9._/:@+-]*) log "box pending printed something else than a version and two images"; exit 0 ;; esac
done

result() {
	docker run --rm -v "$volume:/node" --entrypoint /opt/fridolin/fridolin "$1" box result --state-dir /node "$2" "${3:-}" >>update.log 2>&1 ||
		log "recording the result failed"
}

log "updating to $version: $node"
if ! docker pull -q "$node" >>update.log 2>&1; then
	log "pulling $node failed"
	result "$current" failed "pulling the node's image failed"
	exit 0
fi
if grep -q '^COMPOSE_PROFILES=fride' .env && ! docker pull -q "$fride" >>update.log 2>&1; then
	log "pulling $fride failed"
	result "$current" failed "pulling FrIDE's image failed"
	exit 0
fi

cp .env .env.prev
cp compose.yaml compose.yaml.prev
for f in core.yaml seccomp.json; do
	if [ -f "$f" ]; then cp "$f" "$f.prev"; fi
done
sed -e "s|^FRIDOLIN_BOX_NODE_IMAGE=.*|FRIDOLIN_BOX_NODE_IMAGE=$node|" \
	-e "s|^FRIDOLIN_BOX_FRIDE_IMAGE=.*|FRIDOLIN_BOX_FRIDE_IMAGE=$fride|" .env.prev >.env.new
mv .env.new .env
if docker run --rm --entrypoint /opt/fridolin/fridolin "$node" box compose >compose.yaml.new &&
	! grep -q 'docker\.sock' compose.yaml.new; then
	mv compose.yaml.new compose.yaml
	# The core's settings, from an image that has them.
	if docker run --rm --entrypoint /opt/fridolin/fridolin "$node" box core-compose >core.yaml.new 2>/dev/null &&
		! grep -q 'docker\.sock' core.yaml.new &&
		docker run --rm --entrypoint /opt/fridolin/fridolin "$node" box seccomp >seccomp.json.new 2>/dev/null; then
		mv core.yaml.new core.yaml
		mv seccomp.json.new seccomp.json
		grep -q '^COMPOSE_FILE=' .env || echo "COMPOSE_FILE=compose.yaml:core.yaml" >>.env
	else
		rm -f core.yaml.new seccomp.json.new
	fi
	docker run --rm --entrypoint /opt/fridolin/fridolin "$node" box updater sh >update.sh.new && chmod 755 update.sh.new && mv update.sh.new update.sh
else
	rm -f compose.yaml.new
fi

healthy=0
if docker compose up -d --remove-orphans >>update.log 2>&1; then
	i=0
	while [ "$i" -lt 30 ]; do
		sleep 10
		id=$(docker compose ps -q node 2>/dev/null || true)
		if [ -n "$id" ] && [ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null)" = healthy ]; then
			healthy=1
			break
		fi
		i=$((i + 1))
	done
fi

if [ "$healthy" = 1 ]; then
	log "the box runs $version"
	result "$node" ok
	rm -f .env.prev compose.yaml.prev core.yaml.prev seccomp.json.prev
else
	log "the node did not connect to the core within 5 minutes on $version; going back"
	mv .env.prev .env
	mv compose.yaml.prev compose.yaml
	for f in core.yaml seccomp.json; do
		if [ -f "$f.prev" ]; then mv "$f.prev" "$f"; fi
	done
	docker compose up -d --remove-orphans >>update.log 2>&1 || log "starting the old images failed"
	result "$current" failed "the node did not connect to the core within 5 minutes on the new images; back on the old ones"
fi
