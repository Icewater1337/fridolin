package main

import (
	"context"
)

// saveAttachment is the existing command test seam; the transport consumes
// a supplied inbox directly, without reading process state itself.
func saveAttachment(ctx context.Context, dl mediaDownloader, dir string, att attachment, maxBytes int64) (string, error) {
	return saveAttachmentTo(ctx, dl, mediaInboxAt(dir), att, maxBytes)
}
