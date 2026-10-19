package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/google"
)

func TestStrictBytesRejectTrailingValues(t *testing.T) {
	for _, suffix := range []string{` {}`, ` true`, ` garbage`} {
		var value struct{}
		if err := DecodeStrict([]byte(`{}`+suffix), &value); err == nil || !strings.Contains(err.Error(), "single JSON value") {
			t.Fatalf("suffix %q: %v", suffix, err)
		}
	}
}

func TestCalendarRejectsTrailingValuesBeforeAccess(t *testing.T) {
	a := &BrainAPI{}
	for _, method := range []calendarHandler{a.brainCalendarCreate, a.brainCalendarBusy} {
		for _, suffix := range []string{` {}`, ` true`, ` garbage`} {
			r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`+suffix))
			_, err := method(context.Background(), r, google.Calendars{}, func() (*google.Token, error) {
				t.Fatal("malformed body reached token access")
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), "single JSON value") {
				t.Fatalf("suffix %q: %v", suffix, err)
			}
		}
	}
}
