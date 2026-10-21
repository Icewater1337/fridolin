package vault

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// With ManifestEvery, a sync that changed something within the interval
// leaves the manifest due; the next sync after the interval, or a forced
// flush, writes it with every change.
func TestManifestEveryThrottlesItsUploads(t *testing.T) {
	var puts atomic.Int32
	f := newFixture(t, func(o *Options) { o.ManifestEvery = 10 * time.Minute })
	f.cold.Fail = func(op, key string) error {
		if op == "put" && key == manifestKey {
			puts.Add(1)
		}
		return nil
	}
	bg := context.Background()
	f.write("vault/documents/main/a.md", "a", 0)
	if _, err := f.v.Sync(bg); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 1 {
		t.Fatalf("the first change wrote the manifest %d times", puts.Load())
	}
	f.clock.Advance(time.Minute)
	f.write("vault/documents/main/b.md", "b", 0)
	if _, err := f.v.Sync(bg); err != nil {
		t.Fatal(err)
	}
	if err := f.v.FlushManifest(bg, false); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 1 {
		t.Fatalf("a change a minute later wrote the manifest again (%d)", puts.Load())
	}
	f.clock.Advance(10 * time.Minute)
	if err := f.v.FlushManifest(bg, false); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 2 {
		t.Fatalf("the due manifest was not written after the interval (%d)", puts.Load())
	}
	m, err := f.v.readManifest(bg)
	if err != nil || len(m) != 2 {
		t.Fatalf("manifest = %v, %v", m, err)
	}
	// Nothing due: neither a flush nor a forced one writes.
	f.clock.Advance(time.Hour)
	if err := f.v.FlushManifest(bg, true); err != nil || puts.Load() != 2 {
		t.Fatalf("a flush with nothing due wrote (%d, %v)", puts.Load(), err)
	}
	f.write("vault/documents/main/c.md", "c", 0)
	if _, err := f.v.Sync(bg); err != nil || puts.Load() != 3 {
		t.Fatalf("a change after the interval: %d, %v", puts.Load(), err)
	}
	f.clock.Advance(time.Second)
	f.write("vault/documents/main/d.md", "d", 0)
	if _, err := f.v.Sync(bg); err != nil || puts.Load() != 3 {
		t.Fatalf("a change within it: %d, %v", puts.Load(), err)
	}
	if err := f.v.FlushManifest(bg, true); err != nil || puts.Load() != 4 {
		t.Fatalf("the forced flush at stop: %d, %v", puts.Load(), err)
	}
}
