package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

type fake struct{ name string }

func (f fake) Name() string { return f.name }
func (f fake) Deliver(context.Context, Message, io.Reader) error {
	return nil
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fake{"lmtp"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fake{"lmtp"}); err == nil {
		t.Fatal("duplicate registration should fail")
	}
	if _, ok := r.Get("lmtp"); !ok {
		t.Fatal("registered backend not found")
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("unexpected backend found")
	}
}

func TestIsPermanent(t *testing.T) {
	base := errors.New("mailbox does not exist")
	if !IsPermanent(&PermanentError{Err: base}) {
		t.Error("PermanentError not detected")
	}
	if !IsPermanent(fmt.Errorf("wrap: %w", &PermanentError{Err: base})) {
		t.Error("wrapped PermanentError not detected")
	}
	if IsPermanent(base) {
		t.Error("plain error reported as permanent")
	}
}
