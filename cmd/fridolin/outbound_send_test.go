package main

import (
	"context"

	"errors"
	"fmt"

	"strings"
	"testing"
)

func TestSendChunksReportsWhatWentOut(t *testing.T) {
	chunks := []string{"a", "b", "c"}
	fail := errors.New("offline")
	for i := range chunks {
		t.Run(fmt.Sprint("fails at ", i), func(t *testing.T) {
			var sent []string
			err := sendChunks(context.Background(), chunks, 0, func(c string) error {
				if len(sent) == i {
					return fail
				}
				sent = append(sent, c)
				return nil
			})
			var partial *partialSendError
			switch {
			case !errors.Is(err, fail):
				t.Fatalf("err = %v", err)
			case i == 0 && errors.As(err, &partial):
				t.Fatalf("nothing went out, yet %v", err)
			case i > 0 && (!errors.As(err, &partial) || strings.Join(partial.Sent, "") != strings.Join(chunks[:i], "")):
				t.Fatalf("err = %v, want the first %d chunks reported", err, i)
			}
		})
	}
	if err := sendChunks(context.Background(), chunks, 0, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
