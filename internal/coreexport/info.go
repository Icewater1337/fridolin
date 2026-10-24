package coreexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
)

// Info is what an export says of itself beside the archive, at InfoKey:
// readable without the export's keys, so that a core move (#187) can tell
// whose data dir the latest export is (Device, the lease's device id)
// and which move it was made for, as the lease tells who holds the core.
// It names no account and holds nothing of the data.
type Info struct {
	Key       string    `json:"key"`
	Device    string    `json:"device,omitempty"`
	Move      string    `json:"move,omitempty"`
	Version   string    `json:"version,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Bytes is the size of the encrypted archive.
	Bytes int64 `json:"bytes,omitempty"`
}

// InfoKey is where the info of the export at key is:
// core/exports/<time>.json.
func InfoKey(key string) string {
	return strings.TrimSuffix(key, keySuffix) + ".json"
}

// LatestInfo is the info of the export core/exports/latest names. An
// export made before infos existed has none: its Info names the key
// only, and ok is false.
func LatestInfo(ctx context.Context, store Store) (info Info, ok bool, err error) {
	key, err := Latest(ctx, store)
	if err != nil {
		return Info{}, false, err
	}
	rc, err := store.Get(ctx, InfoKey(key))
	if errors.Is(err, fs.ErrNotExist) {
		return Info{Key: key}, false, nil
	}
	if err != nil {
		return Info{}, false, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 64<<10))
	if err != nil {
		return Info{}, false, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, false, fmt.Errorf("%s does not read: %w", InfoKey(key), err)
	}
	if info.Key != key {
		return Info{}, false, fmt.Errorf("%s names %s, not %s", InfoKey(key), info.Key, key)
	}
	return info, true, nil
}
