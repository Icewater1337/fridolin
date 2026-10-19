package main

import (
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/platform"
)

func (b *Bridge) newFilesystemAPI() *httpapi.FilesystemAPI {
	return &httpapi.FilesystemAPI{
		Home:            homeDir,
		BrowseHome:      func() string { return b.cfgs.Boot().Home },
		DataDir:         func() string { return b.cfgs.Boot().DataDir },
		FolderPicker:    platform.FolderPicker,
		RunPicker:       runPicker,
		PickerCancelled: errPickerCancelled,
		PickMu:          &pickMu,
	}
}
