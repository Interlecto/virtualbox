// Package queue defines the persistent delivery queue.
//
// The queue is the durability boundary of the server: once the SMTP listener
// answers "250 OK", the message MUST already be safely stored here (fsync'd)
// until every recipient has been delivered or permanently failed.
package queue

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrEmpty is returned by Claim when no entry is ready.
var ErrEmpty = errors.New("queue: no entry ready")

// Envelope is the SMTP envelope of a queued message.
type Envelope struct {
	// Sender is the envelope MAIL FROM. Empty means the null sender (bounces).
	Sender string
	// Recipient is a single envelope recipient. The queue holds one entry per
	// recipient/action so retries are independent.
	Recipient string
	// Action describes what to do with the message (see internal/policy).
	// It is opaque to the queue and stored verbatim.
	Action []byte
}

// Entry is a queued unit of work: one message body for one recipient/action.
type Entry struct {
	ID          string
	Envelope    Envelope
	ReceivedAt  time.Time
	Attempts    int
	NextAttempt time.Time
	LastError   string
}

// Queue is a durable, retrying message queue.
//
// Implementations must be safe for concurrent use.
type Queue interface {
	// Enqueue durably stores body and one entry per envelope, sharing the
	// same body. It returns the IDs of the created entries. It must not
	// return until data is persisted.
	Enqueue(ctx context.Context, envs []Envelope, body io.Reader) ([]string, error)

	// Claim returns the next entry whose NextAttempt <= now and marks it
	// in-flight. It returns ErrEmpty when nothing is ready.
	Claim(ctx context.Context, now time.Time) (*Entry, error)

	// Body opens the stored message body for an entry.
	Body(ctx context.Context, id string) (io.ReadCloser, error)

	// Ack removes a successfully delivered entry (and its body when no other
	// entry references it).
	Ack(ctx context.Context, id string) error

	// Retry reschedules an in-flight entry after a temporary failure.
	Retry(ctx context.Context, id string, next time.Time, cause error) error

	// Fail removes an entry after a permanent failure or expiry. The caller
	// is responsible for generating the DSN (bounce).
	Fail(ctx context.Context, id string, cause error) error
}
