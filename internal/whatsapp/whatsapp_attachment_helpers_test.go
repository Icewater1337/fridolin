package whatsapp

import (
	"context"
)

// saveAttachment is the existing command test seam; the transport consumes
// a supplied inbox directly, without reading process state itself.
func saveAttachment(ctx context.Context, dl Downloader, dir string, att attachment, maxBytes int64) (string, error) {
	return saveAttachmentTo(ctx, dl, testInbox(dir), att, maxBytes)
}
