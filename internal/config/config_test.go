package config

import "testing"

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
