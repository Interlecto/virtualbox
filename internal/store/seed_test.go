package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/interlecto/virtualbox/internal/config"
	"github.com/interlecto/virtualbox/internal/policy"
)

func TestSeedFromConfig(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	cfg := &config.Config{Domains: []config.Domain{
		{Name: "Example.org", CatchAll: "catch@x.net", Aliases: map[string][]string{
			"info": {"a@x.net", "b@x.net"},
			"ana":  {"ana@x.net"},
		}},
		{Name: "plain.org"},
	}}

	n, err := s.SeedFromConfig(ctx, cfg)
	if err != nil || n != 3 {
		t.Fatalf("first seed: n=%d err=%v", n, err)
	}
	if doms, _ := s.Domains(ctx); !reflect.DeepEqual(doms, []string{"example.org", "plain.org"}) {
		t.Errorf("domains = %v", doms)
	}
	got, _, _ := s.LookupAlias(ctx, "example.org", "info")
	if len(got) != 2 || got[0].Address != "a@x.net" || got[1].Address != "b@x.net" {
		t.Errorf("info = %+v", got)
	}

	// Admin edits survive a restart: seeding never overwrites or deletes.
	s.SetAlias(ctx, "example.org", "info", []policy.Destination{fwd("changed@x.net")})
	s.DeleteAlias(ctx, "example.org", "ana")
	n, err = s.SeedFromConfig(ctx, cfg)
	if err != nil || n != 1 { // only the deleted "ana" is recreated
		t.Fatalf("second seed: n=%d err=%v", n, err)
	}
	got, _, _ = s.LookupAlias(ctx, "example.org", "info")
	if len(got) != 1 || got[0].Address != "changed@x.net" {
		t.Errorf("seed overwrote an existing rule: %+v", got)
	}
}
