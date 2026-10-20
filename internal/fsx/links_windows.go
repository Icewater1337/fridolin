package fsx

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

// LinkCount returns how many names the file at path has, and false when
// that is not known. fi does not hold it on Windows: path is opened again,
// without following a symlink at its end, which a change to path between
// fi and now can get past.
func LinkCount(path string, _ fs.FileInfo) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return 0, false
	}
	return uint64(info.NumberOfLinks), true
}
