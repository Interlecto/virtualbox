// Package queue defines the persistent delivery queue.
//
// The queue is the durability boundary of the server: once the SMTP listener
// answers "250 OK", the message MUST already be safely stored here (fsync'd)
// until every recipient has been delivered or permanently failed.
//
// Delivery semantics are at-least-once: an entry that was claimed but neither
// acknowledged nor rescheduled (worker crash, restart) becomes ready again.
package queue

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrEmpty is returned by Claim when no entry is ready.
	ErrEmpty = errors.New("queue: no entry ready")
	// ErrNotFound is returned when an entry ID is unknown (for example it was
	// already acknowledged).
	ErrNotFound = errors.New("queue: entry not found")
)

// Envelope is the SMTP envelope of a queued message.
type Envelope struct {
	// Sender is the envelope MAIL FROM. Empty means the null sender (bounces).
	Sender string `json:"sender"`
	// Recipient is a single envelope recipient. The queue holds one entry per
	// recipient/action so retries are independent.
	Recipient string `json:"recipient"`
	// Action describes what to do with the message (see internal/policy).
	// It is opaque to the queue and stored verbatim.
	Action []byte `json:"action,omitempty"`
}

// Entry is a queued unit of work: one message body for one recipient/action.
type Entry struct {
	ID string `json:"id"`
	// BodyID identifies the stored body. Entries created by the same Enqueue
	// call share it.
	BodyID   string   `json:"body_id"`
	Envelope Envelope `json:"envelope"`
	// ReceivedAt is when the message was accepted; expiry is measured from it.
	ReceivedAt time.Time `json:"received_at"`
	// Attempts is the number of failed delivery attempts so far.
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt"`
	LastError   string    `json:"last_error,omitempty"`
}

// Queue is a durable, retrying message queue.
//
// Implementations must be safe for concurrent use.
type Queue interface {
	// Enqueue durably stores body and one entry per envelope, sharing the
	// same body. It returns the IDs of the created entries. It must not
	// return until data is persisted. At least one envelope is required.
	Enqueue(ctx context.Context, envs []Envelope, body io.Reader) ([]string, error)

	// Claim returns the next entry whose NextAttempt <= now and marks it
	// in-flight. It returns ErrEmpty when nothing is ready.
	Claim(ctx context.Context, now time.Time) (*Entry, error)

	// Body opens the stored message body for an entry.
	Body(ctx context.Context, id string) (io.ReadCloser, error)

	// Ack removes a successfully delivered entry (and its body when no other
	// entry references it).
	Ack(ctx context.Context, id string) error

	// Retry reschedules an in-flight entry after a temporary failure. It
	// increments Attempts and records cause as LastError.
	Retry(ctx context.Context, id string, next time.Time, cause error) error

	// Fail removes an entry after a permanent failure or expiry. The caller
	// is responsible for generating the DSN (bounce).
	Fail(ctx context.Context, id string, cause error) error
}
