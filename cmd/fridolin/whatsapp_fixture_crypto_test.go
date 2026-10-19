package main

import (
	"io"

	"go.mau.fi/whatsmeow"

	"go.mau.fi/whatsmeow/util/cbcutil"
)

// fakeMACLength is the length of whatsmeow's media MAC.
const fakeMACLength = 10

// decryptInPlace turns the n encrypted bytes in f back into the attachment
// the way whatsmeow's DownloadToFile does: it reads the MAC off the end,
// truncates it away, and decrypts the rest in place, which writes over the
// file with WriteAt and truncates off the padding.
func decryptInPlace(f whatsmeow.File, n int64) error {
	mac := make([]byte, fakeMACLength)
	if _, err := f.ReadAt(mac, n-fakeMACLength); err != nil {
		return err
	}
	if err := f.Truncate(n - fakeMACLength); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := cbcutil.DecryptFile(fakeMediaKey, fakeMediaIV, f); err != nil {
		return err
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}
