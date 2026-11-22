package policy

import (
	"context"
	"fmt"
	"strings"
)

// TagDelimiter separates the base local part from a sub-address tag:
// "user+news@example.org" has base "user" and tag "news".
const TagDelimiter = "+"

// Match values reported in Action.Params["match"], so later stages (and the
// statistics of roadmap 1.10) can tell how a recipient was resolved.
const (
	MatchAlias    = "alias"    // exact rule for the local part (including "user+tag" rules)
	MatchTag      = "tag"      // rule for the base local part, tag stripped
	MatchCatchAll = "catchall" // domain catch-all
)

// Destination is one target of an alias rule.
type Destination struct {
	Kind Kind // KindForward or KindStore
	// Address is the forward target. For KindStore it is an optional
	// mailbox identifier handed to the backend.
	Address string
	// Backend names the storage backend (KindStore only).
	Backend string
}

// Validate checks that d is a well-formed destination.
func (d Destination) Validate() error {
	switch d.Kind {
	case KindForward:
		if _, _, err := SplitAddress(d.Address); err != nil {
			return fmt.Errorf("forward target %q: %w", d.Address, err)
		}
		if d.Backend != "" {
			return fmt.Errorf("forward target %q must not name a backend", d.Address)
		}
	case KindStore:
		if strings.TrimSpace(d.Backend) == "" {
			return fmt.Errorf("store destination needs a backend")
		}
	default:
		return fmt.Errorf("unsupported destination kind %q", d.Kind)
	}
	return nil
}

// Directory is the read side of the configuration store the AliasResolver
// needs. Implementations must be safe for concurrent use.
type Directory interface {
	// DomainExists reports whether domain (lower-case) is registered.
	DomainExists(ctx context.Context, domain string) (bool, error)
	// LookupAlias returns the ordered destinations of the rule for local
	// (lower-case) in domain. An empty local selects the domain catch-all.
	// found is false when no such rule exists.
	LookupAlias(ctx context.Context, domain, local string) (dests []Destination, found bool, err error)
}

// AliasResolver implements Resolver on top of a Directory. For a recipient
// it tries, in order:
//
//  1. the exact local part (so "user+news" can have its own rule),
//  2. the base local part with the "+tag" suffix removed,
//  3. the domain catch-all.
//
// A registered domain with no matching rule yields ErrUnknownRecipient, and an
// unregistered one ErrUnknownDomain. Local parts match case-insensitively.
type AliasResolver struct {
	dir Directory
}

var _ Resolver = (*AliasResolver)(nil)

// NewAliasResolver returns a Resolver backed by dir.
func NewAliasResolver(dir Directory) *AliasResolver {
	return &AliasResolver{dir: dir}
}

// Resolve implements Resolver.
func (r *AliasResolver) Resolve(ctx context.Context, rcpt string) ([]Action, error) {
	local, domain, err := SplitAddress(rcpt)
	if err != nil {
		return nil, ErrUnknownRecipient
	}
	ok, err := r.dir.DomainExists(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("policy: domain lookup: %w", err)
	}
	if !ok {
		return nil, ErrUnknownDomain
	}
	local = strings.ToLower(local)

	type attempt struct {
		local, match, tag string
	}
	attempts := []attempt{{local: local, match: MatchAlias}}
	if i := strings.Index(local, TagDelimiter); i > 0 {
		attempts = append(attempts, attempt{local: local[:i], match: MatchTag, tag: local[i+len(TagDelimiter):]})
	}
	attempts = append(attempts, attempt{local: "", match: MatchCatchAll})

	for _, a := range attempts {
		dests, found, err := r.dir.LookupAlias(ctx, domain, a.local)
		if err != nil {
			return nil, fmt.Errorf("policy: alias lookup: %w", err)
		}
		if !found || len(dests) == 0 {
			continue
		}
		params := map[string]string{"match": a.match}
		if a.tag != "" {
			params["tag"] = a.tag
		}
		return buildActions(dests, params), nil
	}
	return nil, ErrUnknownRecipient
}

// buildActions groups the forward destinations into a single Action (one
// message, several targets, duplicates removed) and emits one Action per
// store destination, preserving the rule's order.
func buildActions(dests []Destination, params map[string]string) []Action {
	var actions []Action
	fwd := -1 // index of the grouped forward action
	seen := make(map[string]bool)
	for _, d := range dests {
		switch d.Kind {
		case KindForward:
			key := strings.ToLower(d.Address)
			if seen[key] {
				continue
			}
			seen[key] = true
			if fwd < 0 {
				actions = append(actions, Action{Kind: KindForward, Params: cloneParams(params)})
				fwd = len(actions) - 1
			}
			actions[fwd].Targets = append(actions[fwd].Targets, d.Address)
		case KindStore:
			p := cloneParams(params)
			if d.Address != "" {
				p["mailbox"] = d.Address
			}
			actions = append(actions, Action{Kind: KindStore, Backend: d.Backend, Params: p})
		}
	}
	return actions
}

func cloneParams(p map[string]string) map[string]string {
	c := make(map[string]string, len(p)+1)
	for k, v := range p {
		c[k] = v
	}
	return c
}
