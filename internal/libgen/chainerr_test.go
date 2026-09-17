package libgen

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestChainMirrorErrorsWithNoFailures covers the case errors.Join produces a nil
// from: an empty mirror list means nothing was tried, so there are no per-mirror
// errors to chain.
//
// Before this, the nil went to a %w verb and fmt rendered the literal
// "%!w(<nil>)" into the message — which says the formatting broke rather than
// what happened, in a string that reaches the operator's log and the model's
// transcript alike. It was found by the platform matrix, in the output of an
// unrelated Windows failure.
func TestChainMirrorErrorsWithNoFailures(t *testing.T) {
	for _, sentinel := range []error{ErrRequestRejected, ErrAllMirrorsFailed} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			err := chainMirrorErrors(sentinel, nil)

			if !errors.Is(err, sentinel) {
				t.Errorf("errors.Is(err, %v) = false; the sentinel a caller matches on was lost", sentinel)
			}
			if strings.Contains(err.Error(), "%!w") {
				t.Errorf("err = %q, want a message rather than a formatting complaint", err)
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Errorf("err = %q, want it to say why there are no per-mirror errors", err)
			}
		})
	}
}

// TestChainMirrorErrorsKeepsTheCauses pins the ordinary path: with per-mirror
// failures, both the sentinel and every cause stay matchable.
func TestChainMirrorErrorsKeepsTheCauses(t *testing.T) {
	first := errors.New("mirror one refused")
	second := errors.New("mirror two timed out")

	err := chainMirrorErrors(ErrAllMirrorsFailed, errors.Join(first, second))

	for _, want := range []error{ErrAllMirrorsFailed, first, second} {
		if !errors.Is(err, want) {
			t.Errorf("errors.Is(err, %v) = false", want)
		}
	}
}

// TestGetWithNoMirrorsReportsCleanly drives the real path rather than the helper,
// because the helper is only correct if get actually routes through it.
func TestGetWithNoMirrorsReportsCleanly(t *testing.T) {
	c := newTestClient(staticMirrors{})

	_, _, err := c.get(context.Background(), "/json.php", nil)
	if err == nil {
		t.Fatal("get() with no mirrors = nil, want an error")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Errorf("err = %q, want a message rather than a formatting complaint", err)
	}
	if !errors.Is(err, ErrRequestRejected) {
		t.Errorf("err = %v, want it to wrap ErrRequestRejected", err)
	}
}
