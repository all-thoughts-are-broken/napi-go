package napi

import (
	"errors"
	"fmt"
	"testing"
)

// An error returned by this package is often wrapped with context before
// anybody inspects it; AsStatus has to see through that.
func TestAsStatusSeesThroughWrapping(t *testing.T) {
	base := &StatusError{Code: StatusPendingException, Message: "An exception is pending"}

	for name, err := range map[string]error{
		"bare":          base,
		"wrapped once":  fmt.Errorf("calling back: %w", base),
		"wrapped twice": fmt.Errorf("handler: %w", fmt.Errorf("calling back: %w", base)),
		"joined":        errors.Join(errors.New("other"), base),
	} {
		code, ok := AsStatus(err)
		if !ok || code != StatusPendingException {
			t.Errorf("%s: AsStatus = (%v, %v), want (napi_pending_exception, true)", name, code, ok)
		}
	}

	for name, err := range map[string]error{
		"nil":       nil,
		"unrelated": errors.New("not a status"),
	} {
		if code, ok := AsStatus(err); ok || code != StatusOK {
			t.Errorf("%s: AsStatus = (%v, %v), want (napi_ok, false)", name, code, ok)
		}
	}
}
