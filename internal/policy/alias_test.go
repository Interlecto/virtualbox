package policy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeDir is an in-memory Directory keyed by "domain|local".
type fakeDir struct {
	domains map[string]bool
	rules   map[string][]Destination
	err     error
	lookups []string
}

func (f *fakeDir) DomainExists(_ context.Context, d string) (bool, error) {
	return f.domains[d], f.err
}

func (f *fakeDir) LookupAlias(_ context.Context, d, l string) ([]Destination, bool, error) {
	f.lookups = append(f.lookups, l)
	r, ok := f.rules[d+"|"+l]
	return r, ok, f.err
}

func fwd(addr string) Destination { return Destination{Kind: KindForward, Address: addr} }

func newFake() *fakeDir {
	return &fakeDir{
		domains: map[string]bool{"example.org": true, "closed.org": true},
		rules: map[string][]Destination{
			"example.org|info":     {fwd("a@x.net"), fwd("b@x.net"), fwd("A@X.net")},
			"example.org|ana":      {fwd("ana@home.net")},
			"example.org|ana+news": {fwd("news@home.net")},
			"example.org|archive":  {{Kind: KindStore, Backend: "imap", Address: "arch"}, fwd("copy@x.net")},
			"example.org|":         {fwd("catch@x.net")},
			"closed.org|someone":   {fwd("s@x.net")},
		},
	}
}

func TestAliasResolve(t *testing.T) {
	r := NewAliasResolver(newFake())
	ctx := context.Background()

	cases := []struct {
		name, rcpt string
		want       []Action
	}{
		{"multiple targets deduplicated", "Info@Example.ORG", []Action{
			{Kind: KindForward, Targets: []string{"a@x.net", "b@x.net"}, Params: map[string]string{"match": MatchAlias}},
		}},
		{"plain alias", "ana@example.org", []Action{
			{Kind: KindForward, Targets: []string{"ana@home.net"}, Params: map[string]string{"match": MatchAlias}},
		}},
		{"tag stripped to base", "ana+shopping@example.org", []Action{
			{Kind: KindForward, Targets: []string{"ana@home.net"}, Params: map[string]string{"match": MatchTag, "tag": "shopping"}},
		}},
		{"exact tag rule wins over base", "ana+news@example.org", []Action{
			{Kind: KindForward, Targets: []string{"news@home.net"}, Params: map[string]string{"match": MatchAlias}},
		}},
		{"store plus forward keeps order", "archive@example.org", []Action{
			{Kind: KindStore, Backend: "imap", Params: map[string]string{"match": MatchAlias, "mailbox": "arch"}},
			{Kind: KindForward, Targets: []string{"copy@x.net"}, Params: map[string]string{"match": MatchAlias}},
		}},
		{"catch-all for unknown local part", "nobody@example.org", []Action{
			{Kind: KindForward, Targets: []string{"catch@x.net"}, Params: map[string]string{"match": MatchCatchAll}},
		}},
		{"unknown base with tag falls to catch-all", "ghost+x@example.org", []Action{
			{Kind: KindForward, Targets: []string{"catch@x.net"}, Params: map[string]string{"match": MatchCatchAll}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, tc.rcpt)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestAliasResolveErrors(t *testing.T) {
	r := NewAliasResolver(newFake())
	ctx := context.Background()

	if _, err := r.Resolve(ctx, "x@other.net"); !errors.Is(err, ErrUnknownDomain) {
		t.Errorf("foreign domain: %v", err)
	}
	// Registered domain, no rule and no catch-all.
	if _, err := r.Resolve(ctx, "nobody@closed.org"); !errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("closed domain: %v", err)
	}
	if _, err := r.Resolve(ctx, "garbage"); !errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("malformed: %v", err)
	}
}

func TestAliasResolveLeadingPlusIsNotATag(t *testing.T) {
	f := newFake()
	r := NewAliasResolver(f)
	if _, err := r.Resolve(context.Background(), "+odd@example.org"); err != nil {
		t.Fatal(err) // reaches the catch-all
	}
	for _, l := range f.lookups {
		if l == "" {
			continue
		}
		if l != "+odd" {
			t.Errorf("unexpected lookup %q", l)
		}
	}
}

func TestAliasResolveBackendErrorIsNotAnAnswer(t *testing.T) {
	boom := errors.New("db down")
	f := newFake()
	f.err = boom
	_, err := NewAliasResolver(f).Resolve(context.Background(), "info@example.org")
	if !errors.Is(err, boom) || errors.Is(err, ErrUnknownDomain) || errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("want wrapped backend error, got %v", err)
	}
}

func TestDestinationValidate(t *testing.T) {
	good := []Destination{fwd("a@b.org"), {Kind: KindStore, Backend: "imap"}}
	for _, d := range good {
		if err := d.Validate(); err != nil {
			t.Errorf("%+v: %v", d, err)
		}
	}
	bad := []Destination{
		fwd("nope"),
		{Kind: KindForward, Address: "a@b.org", Backend: "imap"},
		{Kind: KindStore},
		{Kind: KindAutoReply, Address: "a@b.org"},
	}
	for _, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("%+v should be invalid", d)
		}
	}
}
