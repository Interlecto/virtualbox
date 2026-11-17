package config

import (
	"path/filepath"
	"testing"
)

func TestValidate(t *testing.T) {
	good := Config{
		Hostname: "mx.example.org",
		DataDir:  "./data",
		Domains:  []Domain{{Name: "example.org"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]Config{
		"no hostname": {DataDir: "d"},
		"no data dir": {Hostname: "h"},
		"empty domain": {Hostname: "h", DataDir: "d",
			Domains: []Domain{{Name: " "}}},
		"duplicate domain": {Hostname: "h", DataDir: "d",
			Domains: []Domain{{Name: "A.org"}, {Name: "a.ORG"}}},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestHasDomain(t *testing.T) {
	c := Config{Domains: []Domain{{Name: "Example.org"}}}
	if !c.HasDomain("example.ORG") {
		t.Error("HasDomain should be case-insensitive")
	}
	if c.HasDomain("other.org") {
		t.Error("unexpected domain match")
	}
}

func TestValidateTLSAndLimits(t *testing.T) {
	base := Config{Hostname: "h", DataDir: "d"}

	c := base
	c.TLS.CertFile = "cert.pem"
	if err := c.Validate(); err == nil {
		t.Error("cert without key should be rejected")
	}
	c.TLS.KeyFile = "key.pem"
	if err := c.Validate(); err != nil {
		t.Errorf("cert and key together should be valid: %v", err)
	}

	c = base
	c.MX.MaxRecipients = -1
	if err := c.Validate(); err == nil {
		t.Error("negative limit should be rejected")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatalf("config.example.json must stay valid: %v", err)
	}
	if !c.HasDomain("example.org") {
		t.Error("example domain missing")
	}
}

func TestValidateAliases(t *testing.T) {
	mk := func(d Domain) Config { return Config{Hostname: "h", DataDir: "d", Domains: []Domain{d}} }

	good := mk(Domain{Name: "a.org", CatchAll: "x@b.org", Aliases: map[string][]string{"info": {"p@b.org", "q@b.org"}, "ana+news": {"n@b.org"}}})
	if err := good.Validate(); err != nil {
		t.Fatalf("valid aliases rejected: %v", err)
	}

	cases := map[string]Domain{
		"bad catch-all":    {Name: "a.org", CatchAll: "nope"},
		"empty local":      {Name: "a.org", Aliases: map[string][]string{"": {"p@b.org"}}},
		"local with space": {Name: "a.org", Aliases: map[string][]string{"a b": {"p@b.org"}}},
		"local with @":     {Name: "a.org", Aliases: map[string][]string{"a@b": {"p@b.org"}}},
		"no targets":       {Name: "a.org", Aliases: map[string][]string{"info": {}}},
		"bad target":       {Name: "a.org", Aliases: map[string][]string{"info": {"nope"}}},
	}
	for name, d := range cases {
		c := mk(d)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDatabasePath(t *testing.T) {
	c := Config{DataDir: "data"}
	if got := c.DatabasePath(); got != filepath.Join("data", "virtualbox.db") {
		t.Errorf("default = %q", got)
	}
	c.DBPath = "/var/lib/vb.db"
	if got := c.DatabasePath(); got != "/var/lib/vb.db" {
		t.Errorf("explicit = %q", got)
	}
}
