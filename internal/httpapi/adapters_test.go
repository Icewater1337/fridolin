package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
)

func TestScriptSendAuthorizesBeforeDelivery(t *testing.T) {
	for _, stage := range []string{"ok", "authorize", "scope"} {
		calls := []string{}
		api := ScriptAPI{
			Authorize: func(*http.Request) (chat.Principal, error) {
				calls = append(calls, "authorize")
				if stage == "authorize" {
					return chat.Principal{}, errors.New("bad token")
				}
				return chat.Principal{}, nil
			},
			Recipient: func(context.Context, string) (string, error) {
				calls = append(calls, "recipient")
				return "normalized", nil
			},
			MaySend: func(chat.Principal, string) error {
				calls = append(calls, "scope")
				if stage == "scope" {
					return errors.New("outside scope")
				}
				return nil
			},
			SendExternal: func(_ context.Context, _ chat.Principal, jid, text string) (string, error) {
				calls = append(calls, "send")
				if jid != "normalized" || text != "hello" {
					t.Fatalf("send %q %q", jid, text)
				}
				return "request-id", nil
			},
		}
		w := httptest.NewRecorder()
		api.Send(w, httptest.NewRequest("POST", "/send", strings.NewReader(`{"jid":"recipient","text":"hello"}`)))
		want, status := "authorize,recipient,scope,send", 200
		switch stage {
		case "authorize":
			want, status = "authorize", 401
		case "scope":
			want, status = "authorize,recipient,scope", 403
		}
		if strings.Join(calls, ",") != want || w.Code != status {
			t.Fatalf("%s: %v %d", stage, calls, w.Code)
		}
		if stage == "ok" && w.Body.String() != "{\"otp_request\":\"request-id\",\"status\":\"sent\"}\n" {
			t.Fatal(w.Body.String())
		}
	}
}

func TestDevInboundNormalizesAndReportsAdmission(t *testing.T) {
	for _, reason := range []string{"", "not allowed"} {
		var received DevInboundRequest
		api := DevAPI{HasBrain: func(id string) bool { return id == "notes" }, Submit: func(r DevInboundRequest) string { received = r; return reason }}
		w := httptest.NewRecorder()
		api.Inbound(w, httptest.NewRequest("POST", "/api/dev/inbound", strings.NewReader(`{"jid":"15551234567","brain":" Notes ","text":"hello"}`)))
		if w.Code != 202 || received.Brain != "notes" || received.Sender != "15551234567" {
			t.Fatalf("%d %+v", w.Code, received)
		}
		if reason != "" && !strings.Contains(w.Body.String(), `"status":"ignored"`) {
			t.Fatal(w.Body.String())
		}
	}
}

func TestFolderPickerSerializesAndReleasesAfterCancellation(t *testing.T) {
	cancelled := errors.New("cancelled")
	var mu sync.Mutex
	calls := 0
	api := FilesystemAPI{Home: func() string { return "home" }, FolderPicker: func(string) []string { return []string{"picker"} }, PickMu: &mu, PickerCancelled: cancelled,
		RunPicker: func(ctx context.Context, argv []string) (string, error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Error("picker has no deadline")
			}
			return "", cancelled
		}}
	mu.Lock()
	w := httptest.NewRecorder()
	api.Pick(w, httptest.NewRequest("POST", "/pick", nil))
	if w.Code != 409 || calls != 0 {
		t.Fatalf("busy picker: %d calls=%d", w.Code, calls)
	}
	mu.Unlock()
	w = httptest.NewRecorder()
	api.Pick(w, httptest.NewRequest("POST", "/pick", nil))
	if w.Code != 200 || w.Body.String() != "{\"cancelled\":true}\n" || calls != 1 {
		t.Fatalf("cancelled: %d %s calls=%d", w.Code, w.Body, calls)
	}
	if !mu.TryLock() {
		t.Fatal("picker retained lock")
	}
	mu.Unlock()
}
