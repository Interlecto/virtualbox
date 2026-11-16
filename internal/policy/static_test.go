package policy

import (
	"context"
	"errors"
	"testing"
)

func TestSplitAddress(t *testing.T) {
	local, domain, err := SplitAddress("Juan.Perez@Example.ORG.")
	if err != nil || local != "Juan.Perez" || domain != "example.org" {
		t.Fatalf("got %q %q %v", local, domain, err)
	}
	for _, bad := range []string{"", "nodomain", "@example.org", "user@", "a b@example.org", "u@x.org>"} {
		if _, _, err := SplitAddress(bad); err == nil {
			t.Errorf("SplitAddress(%q) should fail", bad)
		}
	}
}

func TestStaticResolver(t *testing.T) {
	r := NewStaticResolver(map[string]string{
		"Example.org": "boss@example.org",
		"closed.org":  "",
	})
	ctx := context.Background()

	actions, err := r.Resolve(ctx, "anyone@EXAMPLE.org")
	if err != nil || len(actions) != 1 || actions[0].Kind != KindForward || actions[0].Targets[0] != "boss@example.org" {
		t.Fatalf("catch-all: %+v %v", actions, err)
	}
	if _, err := r.Resolve(ctx, "x@closed.org"); !errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("closed domain: %v", err)
	}
	if _, err := r.Resolve(ctx, "x@other.net"); !errors.Is(err, ErrUnknownDomain) {
		t.Errorf("foreign domain: %v", err)
	}
	if _, err := r.Resolve(ctx, "garbage"); !errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("malformed: %v", err)
	}
}
