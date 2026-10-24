package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/coreexport"
	"github.com/Icewater1337/fridolin/internal/lease"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/provenance"
)

// What a core move (#187; docs/CORE.md, "The core moves itself") has the
// core commands do: a node agent runs these with its own binary, so each
// answers in JSON (nodeproto's types) with --json.

// moveRehearsalVolume is the scratch volume of core rehearse: never the
// core's own (fridolin-data), nor core migrate's rehearsal.
const moveRehearsalVolume = "fridolin-move-rehearsal"

func exportInfo(i coreexport.Info) nodeproto.CoreExportInfo {
	return nodeproto.CoreExportInfo{Key: i.Key, Device: i.Device, Move: i.Move, Version: i.Version, Hostname: i.Hostname, CreatedAt: i.CreatedAt, Bytes: i.Bytes}
}

// coreStatusJSON is core status --json: this data dir's device id, the
// mark of a takeover, the lease and the latest export's info.
func coreStatusJSON(ctx context.Context, dataDir string, f insideFlags, env coreEnv) error {
	id, err := lease.DeviceID(dataDir)
	if err != nil {
		return err
	}
	out := nodeproto.CoreRuntime{Device: id}
	if by, err := os.ReadFile(filepath.Join(config.Layout{Root: dataDir}.Core(), app.SupersededFile)); err == nil {
		out.Superseded = strings.TrimSpace(string(by))
	}
	st, _, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	r, ok, err := lease.Read(ctx, st)
	if err != nil {
		return err
	}
	if ok {
		_, stale := leaseTimings(f)
		state := nodeproto.LeaseLive
		switch {
		case r.Released:
			state = nodeproto.LeaseReleased
		case !r.Live(time.Now(), stale):
			state = nodeproto.LeaseStale
		}
		out.Lease = &nodeproto.CoreLease{Device: r.DeviceID, Hostname: r.Hostname, Version: r.Version, State: state, RenewedAt: r.RenewedAt}
	}
	if info, _, err := coreexport.LatestInfo(ctx, st); err == nil {
		i := exportInfo(info)
		out.Latest = &i
	}
	return json.NewEncoder(env.stdout).Encode(out)
}

// coreRelease is core release: it frees the lease when this data dir
// holds it, for a core that is not running.
func coreRelease(ctx context.Context, f insideFlags, env coreEnv) error {
	st, where, err := coreStoreFrom(f.store, f.bucket, env.environ)
	if err != nil {
		return err
	}
	dataDir, err := insideDataDir(f, env)
	if err != nil {
		return err
	}
	id, err := lease.DeviceID(dataDir)
	if err != nil {
		return err
	}
	ok, err := lease.ReleaseDevice(ctx, st, id, time.Now())
	if err != nil {
		return err
	}
	if ok {
		fmt.Fprintf(env.stdout, "Released the core lease of this device (%s) in %s.\n", id, where)
	} else {
		fmt.Fprintf(env.stdout, "This device (%s) does not hold the core lease in %s: nothing to release.\n", id, where)
	}
	return nil
}

// pullFlags are core pull's and core rehearse's.
type pullFlags struct {
	image         string
	check, asJSON bool
}

// corePull is core pull: check the image's provenance by its digest,
// pull it and tag it ref (fridolin-core:<this version>) for core start;
// --check only says whether ref is here.
func corePull(ctx context.Context, p pullFlags, ref string, env coreEnv, d hostDeps) int {
	answer := func(res nodeproto.CoreImageResult, code int) int {
		if p.asJSON {
			_ = json.NewEncoder(env.stdout).Encode(res)
		} else if res.Note != "" {
			fmt.Fprintln(env.stdout, res.Note)
		}
		return code
	}
	present := d.run(ctx, d.docker, []string{"image", "inspect", "--format", "{{.Id}}", ref}, d.baseEnv, io.Discard, io.Discard) == nil
	if p.check || (present && p.image == "") {
		note := ref + " is here"
		if !present {
			note = ref + " is not here"
		}
		return answer(nodeproto.CoreImageResult{Image: p.image, Present: present, Note: note}, 0)
	}
	if err := nodeproto.CheckImageDigest(p.image); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core pull: --from: %v\n", err)
		return 2
	}
	registry, _, _ := strings.Cut(p.image, "/fridolin-core@")
	repo := provenance.Repo("", registry)
	if repo == "" || !strings.Contains(p.image, "/fridolin-core@") {
		fmt.Fprintf(env.stderr, "fridolin core pull: %s is not a fridolin-core image of ghcr.io/OWNER\n", p.image)
		return 2
	}
	if d.verify == nil {
		fmt.Fprintln(env.stderr, "fridolin core pull: cannot check the image's provenance here")
		return 1
	}
	start := time.Now()
	if err := d.verify(ctx, p.image, repo); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core pull: %v\n", err)
		return 1
	}
	out := env.stderr // docker's progress, never on stdout under --json
	if err := d.run(ctx, d.docker, []string{"pull", p.image}, d.baseEnv, out, env.stderr); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core pull: %v\n", err)
		return 1
	}
	if err := d.run(ctx, d.docker, []string{"tag", p.image, ref}, d.baseEnv, out, env.stderr); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core pull: tagging it %s: %v\n", ref, err)
		return 1
	}
	return answer(nodeproto.CoreImageResult{Image: p.image, Present: true, Pulled: true, Seconds: int(time.Since(start).Seconds()),
		Note: fmt.Sprintf("Pulled %s (provenance: %s, %s) and tagged it %s", p.image, repo, provenance.Workflow, ref)}, 0)
}

// coreRehearse is core rehearse: import the latest export into the
// scratch volume moveRehearsalVolume, which import's checks (every member,
// integrity_check on every database) cover, then remove the volume. The
// one-off container runs on that volume only: never fridolin-data, and
// import never touches the lease.
func coreRehearse(ctx context.Context, asJSON bool, path, override, armor string, childEnv []string, env coreEnv, d hostDeps) int {
	rm := func() error {
		return d.run(ctx, d.docker, []string{"volume", "rm", "-f", moveRehearsalVolume}, d.baseEnv, io.Discard, env.stderr)
	}
	_ = rm() // one left from a rehearsal that was cut off
	start := time.Now()
	scratch := append(append([]string{}, childEnv...), "FRIDOLIN_VOLUME="+moveRehearsalVolume)
	err := d.run(ctx, d.docker, append(composeArgs(path, override, armor), "run", "--rm", "--no-deps", "core", "core", "import", "--latest", "--rehearsal"), scratch, env.stderr, env.stderr)
	res := nodeproto.CoreRehearsal{OK: err == nil, Seconds: int(time.Since(start).Seconds())}
	if err != nil {
		res.Note = "the import failed: " + err.Error()
	}
	if rerr := rm(); rerr != nil {
		res.Note = strings.TrimPrefix(res.Note+"; removing the scratch volume "+moveRehearsalVolume+" failed: "+rerr.Error(), "; ")
		res.OK = false
	}
	if asJSON {
		_ = json.NewEncoder(env.stdout).Encode(res)
	} else if res.OK {
		fmt.Fprintf(env.stdout, "The latest export imported and checked in %ds; the scratch volume is removed.\n", res.Seconds)
	} else {
		fmt.Fprintf(env.stdout, "The rehearsal failed: %s\n", res.Note)
	}
	if !res.OK {
		return 1
	}
	return 0
}
