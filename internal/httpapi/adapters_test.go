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

// /otp/take hands a code only to the scripts token, and only the one that
// answers the request named; every other answer leaves the code out.
func TestTakeOTPGivesCodesOnlyToScripts(t *testing.T) {
	const id = "0123456789abcdef"
	tokenRequired := errors.New("a token is required")
	cases := []struct {
		name, body string
		who        chat.Principal
		authErr    error
		takeErr    error
		waiting    bool
		want       int
	}{
		{"no token", `{"request":"` + id + `"}`, chat.Principal{Kind: chat.CallerAnonymous}, nil, nil, true, http.StatusUnauthorized},
		{"a token the bridge did not give out", `{"request":"` + id + `"}`, chat.Principal{}, errors.New("bad token"), nil, true, http.StatusUnauthorized},
		{"a brain's run", `{"request":"` + id + `"}`, chat.Principal{Kind: chat.CallerBrain, Brain: "p"}, nil, nil, true, http.StatusForbidden},
		{"no request", `{}`, chat.Principal{Kind: chat.CallerScript}, nil, nil, true, http.StatusBadRequest},
		{"not an id", `{"request":"../p"}`, chat.Principal{Kind: chat.CallerScript}, nil, nil, true, http.StatusBadRequest},
		{"no code waiting", `{"request":"` + id + `"}`, chat.Principal{Kind: chat.CallerScript}, nil, nil, false, http.StatusNotFound},
		{"the store failed", `{"request":"` + id + `"}`, chat.Principal{Kind: chat.CallerScript}, nil, errors.New("disk"), true, http.StatusInternalServerError},
		{"the script's own request", `{"request":"` + id + `"}`, chat.Principal{Kind: chat.CallerScript}, nil, nil, true, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := ScriptAPI{
				Authorize:     func(*http.Request) (chat.Principal, error) { return tc.who, tc.authErr },
				TokenRequired: tokenRequired,
				TakeCode: func(request string) (OTPCodeResponse, bool, error) {
					if request != id {
						t.Fatalf("took the code of %q", request)
					}
					if tc.takeErr != nil {
						return OTPCodeResponse{Code: "483920"}, false, tc.takeErr
					}
					return OTPCodeResponse{Code: "483920", Request: id}, tc.waiting, nil
				},
			}
			w := httptest.NewRecorder()
			api.TakeOTP(w, httptest.NewRequest("POST", "/otp/take", strings.NewReader(tc.body)))
			if w.Code != tc.want {
				t.Fatalf("status %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if has := strings.Contains(w.Body.String(), "483920"); has != (tc.want == http.StatusOK) {
				t.Errorf("body %s: the code is there %v, want only with 200", w.Body.String(), has)
			}
		})
	}
}
