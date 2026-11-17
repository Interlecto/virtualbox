package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/interlecto/virtualbox/internal/policy"
)

func fwd(addr string) policy.Destination {
	return policy.Destination{Kind: policy.KindForward, Address: addr}
}

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDomains(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	for _, d := range []string{"B.org", "a.org.", "b.org"} { // duplicate and normalisation
		if err := s.AddDomain(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Domains(ctx)
	if !reflect.DeepEqual(got, []string{"a.org", "b.org"}) {
		t.Errorf("Domains = %v", got)
	}
	if ok, _ := s.DomainExists(ctx, "A.ORG"); !ok {
		t.Error("DomainExists is case-insensitive")
	}
	if ok, _ := s.DomainExists(ctx, "c.org"); ok {
		t.Error("unexpected domain")
	}
	if err := s.AddDomain(ctx, "bad domain"); err == nil {
		t.Error("invalid domain accepted")
	}
	if err := s.RemoveDomain(ctx, "c.org"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveDomain missing: %v", err)
	}
}

func TestSetLookupReplaceDelete(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")

	first := []policy.Destination{fwd("a@x.net"), {Kind: policy.KindStore, Backend: "imap", Address: "box"}}
	if err := s.SetAlias(ctx, "Example.org", "Info", first); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.LookupAlias(ctx, "example.org", "INFO")
	if err != nil || !found || !reflect.DeepEqual(got, first) {
		t.Fatalf("lookup: %+v %v %v", got, found, err)
	}

	// Replacing keeps a single rule with only the new destinations.
	second := []policy.Destination{fwd("z@x.net")}
	if err := s.SetAlias(ctx, "example.org", "info", second); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.LookupAlias(ctx, "example.org", "info")
	if !reflect.DeepEqual(got, second) {
		t.Errorf("after replace: %+v", got)
	}

	if err := s.DeleteAlias(ctx, "example.org", "info"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.LookupAlias(ctx, "example.org", "info"); found {
		t.Error("alias still present")
	}
	if err := s.DeleteAlias(ctx, "example.org", "info"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestSetAliasValidation(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")

	if err := s.SetAlias(ctx, "nope.org", "x", []policy.Destination{fwd("a@b.org")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown domain: %v", err)
	}
	if err := s.SetAlias(ctx, "example.org", "x", nil); err == nil {
		t.Error("empty destinations accepted")
	}
	if err := s.SetAlias(ctx, "example.org", "x", []policy.Destination{fwd("garbage")}); err == nil {
		t.Error("invalid destination accepted")
	}
	if err := s.SetAlias(ctx, "example.org", "a b", []policy.Destination{fwd("a@b.org")}); err == nil {
		t.Error("invalid local part accepted")
	}
	// A failed replace must not destroy the previous rule.
	s.SetAlias(ctx, "example.org", "keep", []policy.Destination{fwd("a@b.org")})
	s.SetAlias(ctx, "example.org", "keep", []policy.Destination{fwd("garbage")})
	if _, found, _ := s.LookupAlias(ctx, "example.org", "keep"); !found {
		t.Error("failed SetAlias removed the existing rule")
	}
}

func TestSetAliasIfMissing(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")

	created, err := s.SetAliasIfMissing(ctx, "example.org", "", []policy.Destination{fwd("one@x.net")})
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	created, err = s.SetAliasIfMissing(ctx, "example.org", "", []policy.Destination{fwd("two@x.net")})
	if err != nil || created {
		t.Fatalf("second: %v %v", created, err)
	}
	got, _, _ := s.LookupAlias(ctx, "example.org", "")
	if len(got) != 1 || got[0].Address != "one@x.net" {
		t.Errorf("existing rule overwritten: %+v", got)
	}
}

func TestRemoveDomainCascades(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")
	s.SetAlias(ctx, "example.org", "a", []policy.Destination{fwd("a@b.org")})
	if err := s.RemoveDomain(ctx, "example.org"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT (SELECT count(*) FROM aliases) + (SELECT count(*) FROM alias_destinations)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d orphan rows left", n)
	}
}

func TestListAliases(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")
	s.SetAlias(ctx, "example.org", "zed", []policy.Destination{fwd("z@b.org")})
	s.SetAlias(ctx, "example.org", "", []policy.Destination{fwd("c@b.org")})
	s.SetAlias(ctx, "example.org", "amy", []policy.Destination{fwd("1@b.org"), fwd("2@b.org")})

	got, err := s.ListAliases(ctx, "example.org")
	if err != nil {
		t.Fatal(err)
	}
	want := []Alias{
		{Local: "", Dests: []policy.Destination{fwd("c@b.org")}},
		{Local: "amy", Dests: []policy.Destination{fwd("1@b.org"), fwd("2@b.org")}},
		{Local: "zed", Dests: []policy.Destination{fwd("z@b.org")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestPersistenceAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vb.db")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.AddDomain(ctx, "example.org")
	s.SetAlias(ctx, "example.org", "info", []policy.Destination{fwd("a@b.org")})
	s.Close()

	s, err = Open(path) // reopen: migration must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, found, _ := s.LookupAlias(ctx, "example.org", "info"); !found {
		t.Error("data lost across reopen")
	}

	// A database written by a newer binary must be refused.
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(path); err == nil {
		t.Error("newer schema accepted")
	}
}

// TestResolverEndToEnd exercises the AliasResolver against the real store.
func TestResolverEndToEnd(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	s.AddDomain(ctx, "example.org")
	s.SetAlias(ctx, "example.org", "ana", []policy.Destination{fwd("ana@home.net"), fwd("ana@work.net")})
	s.SetAlias(ctx, "example.org", "", []policy.Destination{fwd("catch@home.net")})
	r := policy.NewAliasResolver(s)

	acts, err := r.Resolve(ctx, "Ana+Tag@example.org")
	if err != nil || len(acts) != 1 || !reflect.DeepEqual(acts[0].Targets, []string{"ana@home.net", "ana@work.net"}) ||
		acts[0].Params["match"] != policy.MatchTag || acts[0].Params["tag"] != "tag" {
		t.Fatalf("tag: %+v %v", acts, err)
	}
	if acts, err = r.Resolve(ctx, "other@example.org"); err != nil || acts[0].Params["match"] != policy.MatchCatchAll {
		t.Fatalf("catch-all: %+v %v", acts, err)
	}
	if _, err = r.Resolve(ctx, "x@nowhere.net"); !errors.Is(err, policy.ErrUnknownDomain) {
		t.Fatalf("unknown domain: %v", err)
	}
	s.DeleteAlias(ctx, "example.org", "")
	if _, err = r.Resolve(ctx, "other@example.org"); !errors.Is(err, policy.ErrUnknownRecipient) {
		t.Fatalf("unknown recipient: %v", err)
	}
}
