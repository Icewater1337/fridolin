package app

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// vaultAPIService adapts the vault service to the HTTP adapter's interface.
type vaultAPIService struct{ *vaultService }

func (s vaultAPIService) Status(ctx context.Context) any { return s.vaultService.Status(ctx) }

func (s vaultAPIService) RestoreSecretFolders(ctx context.Context, to, at string, force bool) (any, error) {
	return s.vaultService.RestoreSecretFolders(ctx, secretRestoreRequest{To: to, At: at, Force: force})
}

// targetsAPIService adapts the node targets to the HTTP adapter's
// interface.
type targetsAPIService struct{ *targetService }

func (s targetsAPIService) Add(ctx context.Context, t config.VaultTarget) (any, error) {
	return s.targetService.Add(ctx, t)
}

func (b *App) newVaultAPI() *httpapi.VaultAPI {
	return &httpapi.VaultAPI{Vault: func() httpapi.VaultService { return vaultAPIService{b.vault} },
		Targets: &httpapi.VaultTargetsAPI{Targets: func() httpapi.VaultTargetsService { return targetsAPIService{b.targets} }}}
}
