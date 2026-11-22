// Package store is the SQLite-backed configuration store: registered
// domains, aliases and their destinations.
//
// It holds configuration only, never mail. It implements policy.Directory so
// the AliasResolver can validate recipients during the SMTP session, and it
// offers the write operations the admin tooling will use (roadmap phase 6).
//
// The driver is modernc.org/sqlite (pure Go, no CGO), which keeps Windows
// development and static Linux builds simple.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/interlecto/virtualbox/internal/policy"
)

// ErrNotFound is returned when a domain or alias does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is a configuration store. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

var _ policy.Directory = (*Store)(nil)

// schema is applied incrementally: migrations[i] upgrades to version i+1.
var migrations = []string{
	`CREATE TABLE domains (
		name TEXT PRIMARY KEY
	) STRICT;
	CREATE TABLE aliases (
		id     INTEGER PRIMARY KEY,
		domain TEXT NOT NULL REFERENCES domains(name) ON DELETE CASCADE,
		local  TEXT NOT NULL,           -- lower-case; '' is the domain catch-all
		UNIQUE (domain, local)
	) STRICT;
	CREATE TABLE alias_destinations (
		alias_id INTEGER NOT NULL REFERENCES aliases(id) ON DELETE CASCADE,
		position INTEGER NOT NULL,
		kind     TEXT NOT NULL CHECK (kind IN ('forward', 'store')),
		address  TEXT NOT NULL DEFAULT '',
		backend  TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (alias_id, position)
	) STRICT;`,
}

// Open opens (creating if needed) the database at path and migrates it.
// Use ":memory:" for an ephemeral store in tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if path == ":memory:" {
		// Every new connection would get its own empty database.
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("store: database schema v%d is newer than this binary (v%d)", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migrate to v%d: %w", v+1, err)
		}
		// PRAGMA does not accept bound parameters.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migrate to v%d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migrate to v%d: %w", v+1, err)
		}
	}
	return nil
}

func normDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

// AddDomain registers a domain. It is a no-op if it already exists.
func (s *Store) AddDomain(ctx context.Context, domain string) error {
	domain = normDomain(domain)
	if domain == "" || strings.ContainsAny(domain, " \t\r\n@<>") {
		return fmt.Errorf("store: invalid domain %q", domain)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO domains(name) VALUES (?)`, domain); err != nil {
		return fmt.Errorf("store: add domain: %w", err)
	}
	return nil
}

// RemoveDomain deletes a domain with all its aliases.
func (s *Store) RemoveDomain(ctx context.Context, domain string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM domains WHERE name = ?`, normDomain(domain))
	if err != nil {
		return fmt.Errorf("store: remove domain: %w", err)
	}
	return affected(res)
}

// Domains lists the registered domains in alphabetical order.
func (s *Store) Domains(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM domains ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list domains: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("store: list domains: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DomainExists implements policy.Directory.
func (s *Store) DomainExists(ctx context.Context, domain string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM domains WHERE name = ?`, normDomain(domain)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// LookupAlias implements policy.Directory.
func (s *Store) LookupAlias(ctx context.Context, domain, local string) ([]policy.Destination, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.kind, d.address, d.backend
		FROM aliases a JOIN alias_destinations d ON d.alias_id = a.id
		WHERE a.domain = ? AND a.local = ?
		ORDER BY d.position`, normDomain(domain), strings.ToLower(local))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var dests []policy.Destination
	for rows.Next() {
		var d policy.Destination
		var kind string
		if err := rows.Scan(&kind, &d.Address, &d.Backend); err != nil {
			return nil, false, err
		}
		d.Kind = policy.Kind(kind)
		dests = append(dests, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return dests, len(dests) > 0, nil
}

// SetAlias creates or replaces the rule for local in domain with dests. An
// empty local sets the domain catch-all. The domain must exist and dests must
// be non-empty and valid.
func (s *Store) SetAlias(ctx context.Context, domain, local string, dests []policy.Destination) error {
	domain, local = normDomain(domain), strings.ToLower(strings.TrimSpace(local))
	if strings.ContainsAny(local, " \t\r\n@<>") {
		return fmt.Errorf("store: invalid local part %q", local)
	}
	if len(dests) == 0 {
		return errors.New("store: an alias needs at least one destination")
	}
	for _, d := range dests {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set alias: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM domains WHERE name = ?`, domain).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: domain %q: %w", domain, ErrNotFound)
		}
		return fmt.Errorf("store: set alias: %w", err)
	}
	// Replace the whole rule; destinations cascade-delete with the alias.
	if _, err := tx.ExecContext(ctx, `DELETE FROM aliases WHERE domain = ? AND local = ?`, domain, local); err != nil {
		return fmt.Errorf("store: set alias: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO aliases(domain, local) VALUES (?, ?)`, domain, local)
	if err != nil {
		return fmt.Errorf("store: set alias: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("store: set alias: %w", err)
	}
	for i, d := range dests {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO alias_destinations(alias_id, position, kind, address, backend) VALUES (?, ?, ?, ?, ?)`,
			id, i, string(d.Kind), d.Address, d.Backend); err != nil {
			return fmt.Errorf("store: set alias: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set alias: %w", err)
	}
	return nil
}

// SetAliasIfMissing is SetAlias that leaves an existing rule untouched. It
// reports whether the rule was created. It is used to seed the store from the
// static configuration without overwriting later admin changes.
func (s *Store) SetAliasIfMissing(ctx context.Context, domain, local string, dests []policy.Destination) (bool, error) {
	if _, found, err := s.LookupAlias(ctx, domain, local); err != nil {
		return false, fmt.Errorf("store: set alias: %w", err)
	} else if found {
		return false, nil
	}
	if err := s.SetAlias(ctx, domain, local, dests); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteAlias removes the rule for local in domain (empty local: catch-all).
func (s *Store) DeleteAlias(ctx context.Context, domain, local string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM aliases WHERE domain = ? AND local = ?`,
		normDomain(domain), strings.ToLower(local))
	if err != nil {
		return fmt.Errorf("store: delete alias: %w", err)
	}
	return affected(res)
}

// Alias is one rule as listed by ListAliases.
type Alias struct {
	Local string // '' is the catch-all
	Dests []policy.Destination
}

// ListAliases returns all rules of a domain ordered by local part (the
// catch-all, if any, comes first).
func (s *Store) ListAliases(ctx context.Context, domain string) ([]Alias, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.local, d.kind, d.address, d.backend
		FROM aliases a JOIN alias_destinations d ON d.alias_id = a.id
		WHERE a.domain = ?
		ORDER BY a.local, d.position`, normDomain(domain))
	if err != nil {
		return nil, fmt.Errorf("store: list aliases: %w", err)
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var local, kind string
		var d policy.Destination
		if err := rows.Scan(&local, &kind, &d.Address, &d.Backend); err != nil {
			return nil, fmt.Errorf("store: list aliases: %w", err)
		}
		d.Kind = policy.Kind(kind)
		if n := len(out); n == 0 || out[n-1].Local != local {
			out = append(out, Alias{Local: local})
		}
		out[len(out)-1].Dests = append(out[len(out)-1].Dests, d)
	}
	return out, rows.Err()
}

func affected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
