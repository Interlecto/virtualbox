// Package backend defines pluggable external delivery/storage backends.
//
// virtualbox does not keep mail: after accepting it, the message is handed to
// a backend (an IMAP/LMTP server, a webhook, object storage, an archive).
// Each backend implements Backend and registers itself by name.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Message carries the envelope data a backend needs.
type Message struct {
	ID        string // queue entry ID, useful as an idempotency key
	Sender    string
	Recipient string
}

// Backend delivers or stores a message somewhere outside this server.
type Backend interface {
	// Name is the registry name, e.g. "lmtp", "imap", "webhook", "s3".
	Name() string
	// Deliver must return nil only when the message is safely handed over.
	// Return a PermanentError for failures that must not be retried;
	// any other error is treated as temporary and retried by the queue.
	Deliver(ctx context.Context, msg Message, body io.Reader) error
}

// PermanentError marks a delivery failure that must not be retried.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is (or wraps) a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// Registry holds named backends. The zero value is not usable; use NewRegistry.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]Backend)}
}

// Register adds b under its name. It returns an error on duplicates.
func (r *Registry) Register(b Backend) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.backends[b.Name()]; ok {
		return fmt.Errorf("backend: %q already registered", b.Name())
	}
	r.backends[b.Name()] = b
	return nil
}

// Get returns the backend registered under name.
func (r *Registry) Get(name string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[name]
	return b, ok
}
