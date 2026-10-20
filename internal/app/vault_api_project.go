package app

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/httpapi"
)

// vaultAPIService adapts the vault service to the HTTP adapter's interface.
type vaultAPIService struct{ *vaultService }

func (s vaultAPIService) Status(ctx context.Context) any { return s.vaultService.Status(ctx) }

func (b *App) newVaultAPI() *httpapi.VaultAPI {
	return &httpapi.VaultAPI{Vault: func() httpapi.VaultService { return vaultAPIService{b.vault} }}
}
