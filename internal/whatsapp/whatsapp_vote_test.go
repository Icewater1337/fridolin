package whatsapp

import (
	"crypto/sha256"

	"reflect"

	"testing"
)

func TestMatchVote(t *testing.T) {
	hash := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	options := []string{"Monday 2pm", "Tuesday 3pm", "monday 2pm"}
	cases := []struct {
		name   string
		hashes [][]byte
		want   []string
	}{
		{"one", [][]byte{hash("Tuesday 3pm")}, []string{"Tuesday 3pm"}},
		{"several, in the vote's order", [][]byte{hash("monday 2pm"), hash("Monday 2pm")}, []string{"monday 2pm", "Monday 2pm"}},
		{"taken back", nil, nil},
		{"an option the poll does not have", [][]byte{hash("Friday"), hash("Monday 2pm")}, []string{"Monday 2pm"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matchVote(c.hashes, options); !reflect.DeepEqual(got, c.want) {
				t.Errorf("matchVote = %q, want %q", got, c.want)
			}
		})
	}
}
