package config

import (
	"testing"
	"time"
)

func TestVaultTargets(t *testing.T) {
	t.Parallel()
	ok := VaultTarget{Name: "nas", Kind: TargetNode, Node: "a1b2c3d4e5f6", Folder: `D:\Backups\fridolin`}
	if err := checkTargets([]VaultTarget{ok, {Name: "mac", Kind: "node", Node: "0123456789ab", Folder: "/Volumes/Backup/f", NightlyAt: "23:30", Hourly: true,
		Retention: &TargetRetention{Daily: 7}, CheckDays: 1, ReadDataDays: 2, WindowHours: 3}}); err != nil {
		t.Fatal(err)
	}
	if ok.Night() != "03:00" || ok.Keep() != DefaultTargetRetention || ok.Window() != 26*time.Hour || ok.CheckEvery() != 7*24*time.Hour || ok.TargetKey() != "node:nas" {
		t.Fatal("defaults")
	}
	for name, bad := range map[string]VaultTarget{
		"name":      {Name: "NAS", Kind: "node", Node: ok.Node, Folder: ok.Folder},
		"kind":      {Name: "nas", Kind: "b2", Node: ok.Node, Folder: ok.Folder},
		"node":      {Name: "nas", Kind: "node", Node: "laptop", Folder: ok.Folder},
		"folder":    {Name: "nas", Kind: "node", Node: ok.Node, Folder: "Backups"},
		"time":      {Name: "nas", Kind: "node", Node: ok.Node, Folder: ok.Folder, NightlyAt: "3am"},
		"retention": {Name: "nas", Kind: "node", Node: ok.Node, Folder: ok.Folder, Retention: &TargetRetention{}},
		"window":    {Name: "nas", Kind: "node", Node: ok.Node, Folder: ok.Folder, WindowHours: -1},
	} {
		if err := bad.Check(); err == nil {
			t.Errorf("%s: accepted %+v", name, bad)
		}
	}
	if err := checkTargets([]VaultTarget{ok, ok}); err == nil {
		t.Error("two targets of one name")
	}
}
