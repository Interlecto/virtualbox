package policy

import (
	"context"
	"errors"
	"strings"
)

// SplitAddress splits an address into local part and lower-cased domain.
// It performs only the structural checks a resolver needs; full syntax
// validation already happened in the SMTP parser.
func SplitAddress(addr string) (local, domain string, err error) {
	i := strings.LastIndexByte(addr, '@')
	if i <= 0 || i == len(addr)-1 {
		return "", "", errors.New("policy: malformed address")
	}
	local, domain = addr[:i], addr[i+1:]
	if strings.ContainsAny(addr, " \t\r\n<>") {
		return "", "", errors.New("policy: malformed address")
	}
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return "", "", errors.New("policy: malformed address")
	}
	return local, domain, nil
}

// StaticResolver is a minimal Resolver built from a domain -> catch-all map.
// A registered domain with a catch-all forwards every recipient to it; a
// registered domain without one rejects every recipient; any other domain is
// unknown. It is a placeholder until the full resolver (roadmap 1.3) with
// aliases and multiple targets replaces it.
type StaticResolver struct {
	catchAll map[string]string
}

var _ Resolver = (*StaticResolver)(nil)

// NewStaticResolver builds a StaticResolver. Domain names are case-insensitive.
func NewStaticResolver(catchAll map[string]string) *StaticResolver {
	m := make(map[string]string, len(catchAll))
	for d, target := range catchAll {
		m[strings.ToLower(d)] = target
	}
	return &StaticResolver{catchAll: m}
}

// Resolve implements Resolver.
func (r *StaticResolver) Resolve(_ context.Context, rcpt string) ([]Action, error) {
	_, domain, err := SplitAddress(rcpt)
	if err != nil {
		return nil, ErrUnknownRecipient
	}
	target, ok := r.catchAll[domain]
	if !ok {
		return nil, ErrUnknownDomain
	}
	if target == "" {
		return nil, ErrUnknownRecipient
	}
	return []Action{{Kind: KindForward, Targets: []string{target}}}, nil
}
