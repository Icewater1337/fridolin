package google

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

func TestBuildMIMENonASCII(t *testing.T) {
	t.Parallel()
	raw, err := buildMIME(MailDraft{To: []string{"Jörg Müller <joerg@example.com>"}, Cc: []string{"a@example.com, b@example.com"},
		Subject: "Grüße – Café ☕", Body: "Schöne Grüße\nZeile zwei"}, threading{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); subj != "Grüße – Café ☕" {
		t.Fatalf("subject %q", m.Header.Get("Subject"))
	}
	if strings.ContainsAny(m.Header.Get("Subject"), "üß☕") {
		t.Fatal("subject has raw non-ASCII")
	}
	to, err := m.Header.AddressList("To")
	if err != nil || len(to) != 1 || to[0].Name != "Jörg Müller" || to[0].Address != "joerg@example.com" {
		t.Fatalf("To %v %v", to, err)
	}
	if cc, _ := m.Header.AddressList("Cc"); len(cc) != 2 {
		t.Fatalf("Cc %v", cc)
	}
	body, _ := io.ReadAll(m.Body) // quoted-printable: 7-bit on the wire
	for _, c := range body {
		if c > 127 {
			t.Fatal("body is not 7-bit")
		}
	}
	if !strings.Contains(string(body), "Sch=C3=B6ne") {
		t.Fatalf("body %q", body)
	}
}

func TestBuildMIMERejectsHeaderInjection(t *testing.T) {
	t.Parallel()
	if _, err := buildMIME(MailDraft{To: []string{"a@example.com"}, Subject: "hi\r\nBcc: evil@example.com"}, threading{}); err == nil {
		t.Fatal("a subject with a line break was accepted")
	}
	if _, err := buildMIME(MailDraft{To: []string{"not an address"}}, threading{}); err == nil {
		t.Fatal("a bad address was accepted")
	}
}

func TestBuildMIMEReplyThreading(t *testing.T) {
	t.Parallel()
	raw, err := buildMIME(MailDraft{To: []string{"a@example.com"}, Subject: "Re: x", Body: "ok"},
		threading{messageID: "<m2@example.com>", references: "<m0@example.com> <m1@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := mail.ReadMessage(strings.NewReader(string(raw)))
	if got := m.Header.Get("In-Reply-To"); got != "<m2@example.com>" {
		t.Fatalf("In-Reply-To %q", got)
	}
	if got := m.Header.Get("References"); got != "<m0@example.com> <m1@example.com> <m2@example.com>" {
		t.Fatalf("References %q", got)
	}
}

// gmailFake serves the pieces of the Gmail API the commands use.
func gmailFake(t *testing.T, sent *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gmail/v1/users/me/messages/orig" && r.Method == "GET":
			fmt.Fprint(w, `{"id":"orig","threadId":"T1","payload":{"headers":[
				{"name":"From","value":"=?UTF-8?Q?J=C3=B6rg?= <joerg@example.com>"},
				{"name":"Subject","value":"Termin"},{"name":"Message-ID","value":"<o@example.com>"},
				{"name":"References","value":"<p@example.com>"}]}}`)
		case r.URL.Path == "/gmail/v1/users/me/messages/send" && r.Method == "POST":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			*sent = b
			fmt.Fprint(w, `{"id":"new","threadId":"T1"}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"message":"not faked"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSendMailReplyStaysInThread(t *testing.T) {
	t.Parallel()
	sent := map[string]string{}
	srv := gmailFake(t, &sent)
	g := New(googleTestOptions(Options{CredsDir: t.TempDir, ClientPath: t.TempDir() + "/c.json"}, srv))
	tok := &Token{Token: "x", Scopes: []string{GmailScope}}
	if _, err := g.SendMail(t.Context(), tok, MailDraft{Body: "ja", ReplyTo: "orig"}); err != nil {
		t.Fatal(err)
	}
	if sent["threadId"] != "T1" {
		t.Fatalf("thread %q", sent["threadId"])
	}
	raw, _ := base64.RawURLEncoding.DecodeString(sent["raw"])
	m, _ := mail.ReadMessage(strings.NewReader(string(raw)))
	if m.Header.Get("In-Reply-To") != "<o@example.com>" || m.Header.Get("References") != "<p@example.com> <o@example.com>" || m.Header.Get("Subject") != "Re: Termin" {
		t.Fatalf("headers %v", m.Header)
	}
	if to, _ := m.Header.AddressList("To"); len(to) != 1 || to[0].Address != "joerg@example.com" || to[0].Name != "Jörg" {
		t.Fatalf("To %v", to)
	}
}

func TestGmailNeedsItsScope(t *testing.T) {
	t.Parallel()
	sent := map[string]string{}
	srv := gmailFake(t, &sent)
	g := New(googleTestOptions(Options{CredsDir: t.TempDir, ClientPath: t.TempDir() + "/c.json"}, srv))
	calendarOnly := &Token{Token: "x", Scopes: CalendarScopes()}
	_, err := g.ListMail(t.Context(), calendarOnly, "", 5)
	if !errors.Is(err, ErrNoGmail) {
		t.Fatalf("err %v", err)
	}
	if status, msg := CallProblem(err); status != http.StatusPreconditionFailed || !strings.Contains(msg, "reconnect Google to grant Gmail") {
		t.Fatalf("%d %s", status, msg)
	}
	if len(sent) != 0 {
		t.Fatal("a call reached Gmail without the scope")
	}
}

func TestPlainBodyAndAttachments(t *testing.T) {
	t.Parallel()
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	p := gmailPart{MimeType: "multipart/mixed", Parts: []gmailPart{
		{MimeType: "multipart/alternative", Parts: []gmailPart{
			{MimeType: "text/html"}, {MimeType: "text/plain"}}},
		{MimeType: "application/pdf", Filename: "a.pdf"},
	}}
	p.Parts[0].Parts[0].Body.Data = enc("<p>html</p>")
	p.Parts[0].Parts[1].Body.Data = enc("Grüße")
	p.Parts[1].Body.Size = 12
	text, atts := plainBody(p)
	if text != "Grüße" || len(atts) != 1 || atts[0].Filename != "a.pdf" || atts[0].Size != 12 {
		t.Fatalf("%q %v", text, atts)
	}
	htmlOnly := gmailPart{MimeType: "text/html"}
	htmlOnly.Body.Data = enc("<style>x{}</style><b>hi</b>")
	if text, _ := plainBody(htmlOnly); text != "hi" {
		t.Fatalf("%q", text)
	}
}
