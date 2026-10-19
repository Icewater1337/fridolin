package httpapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These keys come from the map responses before the DTO migration. Compare
// encoded bytes too: map key order, nulls and empty slices are wire contracts.
func TestResponseDTOsKeepMapWireEncoding(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		fields []string
	}{
		{"AuthURLResponse", AuthURLResponse{}, []string{"AuthURL=auth_url"}},
		{"SignInResponse", SignInResponse{}, []string{"Binding=binding", "Status=status"}},
		{"BackupVersionsResponse", BackupVersionsResponse{}, []string{"Brain=brain", "Versions=versions"}},
		{"BuildersResponse", BuildersResponse{}, []string{"Brains=brains"}},
		{"CalendarBusyResponse", CalendarBusyResponse{}, []string{"Busy=busy", "Calendars=calendars"}},
		{"CalendarListResponse", CalendarListResponse{}, []string{"Calendars=calendars", "Restricted=restricted"}},
		{"PickerCancelledResponse", PickerCancelledResponse{}, []string{"Cancelled=cancelled"}},
		{"BuilderProposalResponse", BuilderProposalResponse{}, []string{"Changes=changes", "Proposal=proposal"}},
		{"LoginCodeResponse", LoginCodeResponse{}, []string{"Code=code", "ExpiresAt=expires_at"}},
		{"ConfigResponse", ConfigResponse{}, []string{"Config=config", "Effective=effective", "ETag=etag", "Path=path"}},
		{"PollsResponse", PollsResponse{}, []string{"Count=count", "Polls=polls"}},
		{"CalendarCreatedResponse", CalendarCreatedResponse{}, []string{"Created=created"}},
		{"BrainCreatedResponse", BrainCreatedResponse{}, []string{"Created=created", "ID=id", "Path=path"}},
		{"QRCodeResponse", QRCodeResponse{}, []string{"CreatedAt=created_at", "DataURL=data_url"}},
		{"CalendarDeletedResponse", CalendarDeletedResponse{}, []string{"Deleted=deleted"}},
		{"DeployKeyResponse", DeployKeyResponse{}, []string{"DeployKey=deploy_key"}},
		{"DirectoryResponse", DirectoryResponse{}, []string{"Entries=entries", "Home=home", "Parent=parent", "Path=path"}},
		{"ChatAllowedResponse", ChatAllowedResponse{}, []string{"Entry=entry", "Status=status"}},
		{"ErrorResponse", ErrorResponse{}, []string{"Error=error"}},
		{"ConfigSavedResponse", ConfigSavedResponse{}, []string{"ETag=etag", "RestartRequired=restart_required"}},
		{"PickerCapabilitiesResponse", PickerCapabilitiesResponse{}, []string{"Home=home", "Picker=picker"}},
		{"IngestAcceptedResponse", IngestAcceptedResponse{}, []string{"JID=jid", "Status=status"}},
		{"ChatMembersResponse", ChatMembersResponse{}, []string{"Members=members", "Status=status"}},
		{"SiteSecretsResponse", SiteSecretsResponse{}, []string{"Modules=modules"}},
		{"CalendarDisconnectedResponse", CalendarDisconnectedResponse{}, []string{"Note=note", "Revoked=revoked", "Status=status"}},
		{"GoogleClientSavedResponse", GoogleClientSavedResponse{}, []string{"Note=note", "Status=status", "Type=type"}},
		{"OKResponse", OKResponse{}, []string{"Ok=ok"}},
		{"DeployKeyAddedResponse", DeployKeyAddedResponse{}, []string{"Ok=ok", "Repo=repo"}},
		{"PickerPathResponse", PickerPathResponse{}, []string{"Path=path"}},
		{"SiteProposalsResponse", SiteProposalsResponse{}, []string{"Proposals=proposals"}},
		{"DevIgnoredResponse", DevIgnoredResponse{}, []string{"Reason=reason", "Status=status"}},
		{"ChatRevokedResponse", ChatRevokedResponse{}, []string{"Removed=removed", "Status=status"}},
		{"BuilderSyncResponse", BuilderSyncResponse{}, []string{"Result=result"}},
		{"SiteRolesResponse", SiteRolesResponse{}, []string{"Roles=roles"}},
		{"SiteUsersResponse", SiteUsersResponse{}, []string{"Roles=roles", "Users=users"}},
		{"OperationResponse", OperationResponse{}, []string{"Status=status"}},
		{"RestoreStartedResponse", RestoreStartedResponse{}, []string{"Status=status", "Target=target"}},
		{"RunStoppedResponse", RunStoppedResponse{}, []string{"Stopped=stopped"}},
		{"RepositoryCreatedResponse", RepositoryCreatedResponse{}, []string{"URL=url"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, filled := range []bool{false, true} {
				v := reflect.New(reflect.TypeOf(tc.value)).Elem()
				v.Set(reflect.ValueOf(tc.value))
				if filled {
					for i := 0; i < v.NumField(); i++ {
						f := v.Field(i)
						switch f.Kind() {
						case reflect.String:
							f.SetString("<>& escaped and non-ASCII é")
						case reflect.Bool:
							f.SetBool(true)
						case reflect.Int, reflect.Int64:
							f.SetInt(7)
						case reflect.Slice:
							f.Set(reflect.MakeSlice(f.Type(), 0, 0))
						case reflect.Pointer:
							f.Set(reflect.New(f.Type().Elem()))
						case reflect.Interface:
							f.Set(reflect.ValueOf("<>& payload"))
						}
					}
				}
				old := map[string]any{}
				for _, mapping := range tc.fields {
					name, key, ok := strings.Cut(mapping, "=")
					if !ok {
						t.Fatal(mapping)
					}
					f := v.FieldByName(name)
					if !f.IsValid() {
						t.Fatalf("missing field %s", name)
					}
					old[key] = f.Interface()
				}
				want, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(v.Interface())
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Errorf("filled=%v: got %s; want %s", filled, got, want)
				}
			}
		})
	}
}
