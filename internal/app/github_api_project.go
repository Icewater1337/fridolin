package app

import "github.com/Icewater1337/fridolin/internal/httpapi"

func (b *App) newGitHubAPI() *httpapi.GitHubAPI {
	return &httpapi.GitHubAPI{GitHub: func() httpapi.GitHubService { return b.github }}
}
