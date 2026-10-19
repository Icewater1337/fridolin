package httpapi

type DirectoryEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsBrain bool   `json:"is_brain"`
	IsGit   bool   `json:"is_git"`
}
