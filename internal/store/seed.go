package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/interlecto/virtualbox/internal/config"
	"github.com/interlecto/virtualbox/internal/policy"
)

// SeedFromConfig makes the domains, catch-alls and aliases declared in the
// static configuration exist in the store. It only creates what is missing:
// rules that already exist (for example changed later through the admin
// tooling) are never overwritten, and nothing is deleted. It returns the
// number of rules created.
func (s *Store) SeedFromConfig(ctx context.Context, cfg *config.Config) (int, error) {
	created := 0
	for _, d := range cfg.Domains {
		if err := s.AddDomain(ctx, d.Name); err != nil {
			return created, err
		}
		if d.CatchAll != "" {
			ok, err := s.SetAliasIfMissing(ctx, d.Name, "", []policy.Destination{
				{Kind: policy.KindForward, Address: d.CatchAll},
			})
			if err != nil {
				return created, fmt.Errorf("store: seed catch-all of %s: %w", d.Name, err)
			}
			if ok {
				created++
			}
		}
		locals := make([]string, 0, len(d.Aliases))
		for l := range d.Aliases {
			locals = append(locals, l)
		}
		sort.Strings(locals) // deterministic order
		for _, local := range locals {
			dests := make([]policy.Destination, len(d.Aliases[local]))
			for i, t := range d.Aliases[local] {
				dests[i] = policy.Destination{Kind: policy.KindForward, Address: t}
			}
			ok, err := s.SetAliasIfMissing(ctx, d.Name, local, dests)
			if err != nil {
				return created, fmt.Errorf("store: seed alias %s@%s: %w", local, d.Name, err)
			}
			if ok {
				created++
			}
		}
	}
	return created, nil
}
