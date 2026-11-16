// Package policy resolves what must happen with a message for each recipient.
//
// The Resolver turns a recipient address into an ordered list of Actions
// (forward, catch-all, autoreply, external store, temporary mailbox, list).
// The dispatcher then executes them. Keeping resolution separate from
// execution makes the routing rules testable without any network I/O.
package policy

import (
	"context"
	"errors"
)

var (
	// ErrUnknownDomain means the recipient domain is not registered here.
	// The SMTP listener answers it with a 5xx (never accept-then-bounce).
	ErrUnknownDomain = errors.New("policy: unknown domain")
	// ErrUnknownRecipient means the domain is ours but the local part has no
	// rule and there is no catch-all.
	ErrUnknownRecipient = errors.New("policy: unknown recipient")
)

// Kind identifies the type of an Action.
type Kind string

const (
	// KindForward re-sends the message to one or more other addresses
	// (using SRS so SPF keeps working).
	KindForward Kind = "forward"
	// KindStore hands the message to an external storage backend
	// (LMTP, IMAP APPEND, webhook, S3, ...).
	KindStore Kind = "store"
	// KindTempBox stores the message locally with quota and TTL.
	KindTempBox Kind = "tempbox"
	// KindAutoReply sends an automatic response to the sender.
	KindAutoReply Kind = "autoreply"
	// KindList delivers to a mailing list engine.
	KindList Kind = "list"
)

// Action is one step to apply to a message for one recipient.
type Action struct {
	Kind Kind
	// Targets are destination addresses (forward) or list IDs (list).
	Targets []string
	// Backend names a configured storage backend (store).
	Backend string
	// Params carries kind-specific settings (TTL, quota, reply text, ...).
	Params map[string]string
}

// Resolver maps a recipient address to its actions.
type Resolver interface {
	// Resolve returns the actions for rcpt, or ErrUnknownDomain /
	// ErrUnknownRecipient. Catch-all handling is the Resolver's job.
	Resolve(ctx context.Context, rcpt string) ([]Action, error)
}
