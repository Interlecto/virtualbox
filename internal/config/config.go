// Package config loads and validates the static server configuration.
//
// Dynamic data (aliases, lists, credentials, rules) will live in the
// database, not here. This file only holds what is needed to boot.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Config is the root static configuration.
type Config struct {
	// Hostname is the name announced in EHLO and used as the MX target.
	Hostname string `json:"hostname"`
	// DataDir holds the persistent queue and local state.
	DataDir string `json:"data_dir"`
	Listen  Listen `json:"listen"`
	// Domains are the registered domains this server accepts mail for.
	Domains []Domain `json:"domains"`
}

// Listen holds the listening addresses. An empty address disables the service.
type Listen struct {
	MX            string `json:"mx"`             // inbound SMTP, normally :25
	Submission    string `json:"submission"`     // STARTTLS submission, normally :587
	SubmissionTLS string `json:"submission_tls"` // implicit TLS submission, normally :465
	Admin         string `json:"admin"`          // admin API / UI
}

// Domain is a registered domain.
type Domain struct {
	Name string `json:"name"`
	// CatchAll is the address that receives mail for unknown local parts.
	// Empty means unknown recipients are rejected.
	CatchAll string `json:"catch_all"`
}

// Load reads and validates the JSON configuration at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()

	var c Config
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

// Validate checks the configuration for obvious errors.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Hostname) == "" {
		return errors.New("hostname is required")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("data_dir is required")
	}
	seen := make(map[string]bool, len(c.Domains))
	for _, d := range c.Domains {
		name := strings.ToLower(strings.TrimSpace(d.Name))
		if name == "" {
			return errors.New("domain with empty name")
		}
		if seen[name] {
			return fmt.Errorf("duplicate domain %q", name)
		}
		seen[name] = true
	}
	return nil
}

// HasDomain reports whether name is a registered domain (case-insensitive).
func (c *Config) HasDomain(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, d := range c.Domains {
		if strings.ToLower(d.Name) == name {
			return true
		}
	}
	return false
}
