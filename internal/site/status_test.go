package site

import (
	"encoding/json"
	"testing"
)

func TestSiteStatusSnapshotPreservesMapEncoding(t *testing.T) {
	empty := ""
	for _, full := range []bool{false, true} {
		v := StatusSnapshot{Google: GoogleStatus{Source: "none"}}
		runtime := map[string]any{"node": v.Runtime.Node, "cloudflared": v.Runtime.Cloudflared, "gh": v.Runtime.GH}
		google := map[string]any{"source": v.Google.Source, "redirect_uri": v.Google.RedirectURI}
		old := map[string]any{
			"settings": v.Settings, "runtime": runtime,
			"site":   map[string]any{"process": v.Site.Process, "healthy": v.Site.Healthy, "local_url": v.Site.LocalURL, "sandbox": v.Site.Sandbox},
			"tunnel": map[string]any{"process": v.Tunnel.Process, "token_saved": v.Tunnel.TokenSaved},
			"repo":   map[string]any{"connected": v.Repo.Connected, "github": v.Repo.GitHub},
			"google": google, "accounts": map[string]int{"total": v.Accounts.Total, "admins": v.Accounts.Admins}, "deploy": v.Deploy,
		}
		if full {
			v.DeployKey = &empty
			v.Google.Type = &empty
			v.Runtime.NodeError = &empty
			v.PublicURL = "https://example.com/"
			old["deploy_key"] = ""
			google["type"] = ""
			runtime["node_error"] = ""
			old["public_url"] = v.PublicURL
		}
		got, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("full=%v\ngot %s\nwant %s", full, got, want)
		}
	}
}

func TestProposalSnapshotKeepsEmptyOptionalFields(t *testing.T) {
	empty := ""
	preview := map[string]string{}
	value := ProposalSnapshot{Error: &empty, Preview: &preview}
	old := map[string]any{"changes": []ModuleChange(nil), "error": "", "preview": preview, "proposal": value.Proposal}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %s; want %s", got, want)
	}
}
