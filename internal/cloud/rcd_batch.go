package cloud

import (
	"context"
	"errors"
	"strings"
)

// globEscape escapes rclone's filter glob characters in a key.
func globEscape(key string) string {
	var b strings.Builder
	for _, r := range key {
		if strings.ContainsRune(`*?[]{}\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DeleteKeys implements BatchDeleter with one operations/delete over the
// store's root, filtered to exactly keys: rclone lists the root once
// (with FastList, one Class C per thousand names on B2) and deletes what
// matches, instead of a lookup (a Class B) per object as Delete makes.
// Keys that are not there are no error.
func (r *RcloneRC) DeleteKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	rules := make([]string, 0, len(keys))
	for _, k := range keys {
		if err := ValidKey(k); err != nil {
			return err
		}
		rules = append(rules, "/"+globEscape(k))
	}
	cost := Cost{A: float64(len(keys)), C: float64(1 + len(keys)/1000)}
	err := r.call(ctx, "delete-many", cost, "operations/delete", map[string]any{"fs": r.Remote,
		"_filter": map[string]any{"IncludeRule": rules}}, nil)
	if err != nil {
		err = r.fail("delete-many", "", err)
		if re, ok := errors.AsType[*RcloneError](err); ok && re.notFound() {
			return nil
		}
	}
	return err
}

// DeleteKeys implements BatchDeleter for a folder: one Delete each.
func (d *Dir) DeleteKeys(ctx context.Context, keys []string) error {
	for _, k := range keys {
		if err := d.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
