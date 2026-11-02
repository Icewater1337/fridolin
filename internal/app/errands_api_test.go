package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/store"
)

// TestErrandsDashboard: the Errands page lists every errand with its
// conversation and the people blocked; its Stop ends a live errand as the
// brain's errand_stop does, without a word to the person, and its Unblock
// lifts a block as !unblock does, with an errand line in the audit log.
func TestErrandsDashboard(t *testing.T) {
	t.Parallel()
	h, _ := errandHarness(t)
	ctx := context.Background()
	if err := h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactKnown, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.b.openErrand(ctx, h.ownerRun(), dinner); err != nil {
		t.Fatal(err)
	}
	h.WaitSent(errandJID, "When would dinner")

	var list httpapi.ErrandsResponse
	if w := h.Do("GET", "/api/errands", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("GET /api/errands: %d %s", w.Code, w.Body.String())
	}
	if len(list.Errands) != 1 || len(list.Blocked) != 0 {
		t.Fatalf("the list: %+v", list)
	}
	e := list.Errands[0]
	if e.Brain != "core" || e.Name != "Alex" || e.Number != errandNumber || e.Masked != "+1…21" || e.State != store.ErrandOpen || !e.Live || e.Sent != 1 || e.Closed != nil {
		t.Errorf("the errand: %+v", e)
	}
	var detail httpapi.ErrandDetail
	if w := h.Do("GET", "/api/errands/"+e.ID, nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatalf("GET the errand: %d %s", w.Code, w.Body.String())
	}
	if detail.Goal != dinner.Goal || detail.Brief != dinner.Brief || !detail.ByOwner || len(detail.Lines) != 1 || detail.Lines[0].From != "you" || detail.Lines[0].Text != dinner.Message {
		t.Errorf("the errand's detail: %+v", detail)
	}

	if w := h.Do("POST", "/api/errands/nope/stop", nil); w.Code != http.StatusNotFound {
		t.Errorf("stopping no errand: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/errands/"+e.ID+"/stop", nil); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	if got := h.errandOf(e.ID); got.State != store.ErrandStopped || got.Outcome != "the owner ended it in the dashboard" || got.ClosedAt.IsZero() {
		t.Errorf("after the stop: %+v", got)
	}
	if w := h.Do("POST", "/api/errands/"+e.ID+"/stop", nil); w.Code != http.StatusConflict {
		t.Errorf("stopping it again: %d %s", w.Code, w.Body.String())
	}
	if got := h.SentTo(errandJID); len(got) != 1 {
		t.Errorf("the stop wrote to them: %q", got)
	}

	if err := h.b.store.SaveErrandContact(ctx, store.ErrandContact{Contact: errandNumber, Name: "Alex", Brain: "core", State: store.ContactBlocked, Reason: "asked for Sam's address", UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	list = httpapi.ErrandsResponse{}
	_ = json.Unmarshal(h.Do("GET", "/api/errands", nil).Body.Bytes(), &list)
	if len(list.Blocked) != 1 || list.Blocked[0].Number != errandNumber || list.Blocked[0].Reason != "asked for Sam's address" || list.Blocked[0].Masked != "+1…21" {
		t.Fatalf("blocked: %+v", list.Blocked)
	}
	if w := h.Do("POST", "/api/errand-contacts/Alex/unblock", nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unblocking no number: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/errand-contacts/"+errandNumber+"/unblock", nil); w.Code != http.StatusOK {
		t.Fatalf("unblock: %d %s", w.Code, w.Body.String())
	}
	if _, err := h.b.store.ErrandContactOf(ctx, errandNumber); err == nil {
		t.Error("still blocked after the unblock")
	}
	if w := h.Do("POST", "/api/errand-contacts/"+errandNumber+"/unblock", nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "is not blocked") {
		t.Errorf("unblocking again: %d %s", w.Code, w.Body.String())
	}
	var unblocks []string
	for _, l := range h.auditOf("errand") {
		if l["detail"] == "unblock" {
			if l["by"] != "dashboard" || strings.Contains(l["id"].(string), errandNumber) {
				t.Errorf("the audit's unblock line: %v", l)
			}
			unblocks = append(unblocks, l["outcome"].(string))
		}
	}
	if strings.Join(unblocks, ",") != "unblocked,nothing blocked" {
		t.Errorf("the audit's unblock lines: %q", unblocks)
	}
}
