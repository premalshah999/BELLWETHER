package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tradesys/dashboard/internal/auth"
)

// IssueKey records a newly minted key against a profile.
func (d *DB) IssueKey(ctx context.Context, k auth.Key, name string, role auth.Role, note string) (auth.Profile, error) {
	var p auth.Profile
	var lastUsed, revoked sql.NullTime
	err := d.db.QueryRowContext(ctx, `
		INSERT INTO api_keys (prefix, key_hash, name, role, note)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, prefix, name, role, note, created_at, last_used_at, revoked_at`,
		k.Prefix, k.Hash, name, string(role), note,
	).Scan(&p.ID, &p.Prefix, &p.Name, &p.Role, &p.Note, &p.CreatedAt, &lastUsed, &revoked)
	if err != nil {
		return auth.Profile{}, fmt.Errorf("issue key: %w", err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	return p, nil
}

// ProfileForKey resolves a presented key to its profile.
//
// Two steps on purpose. The prefix selects one row, then the full digest is
// compared in constant time; looking a key up by its digest directly would
// work too, but this keeps the stored prefix useful for logs and for the
// interface without ever putting the secret in an index.
//
// A revoked key resolves to nothing. Returning the profile with a flag would
// invite a caller to forget the check.
func (d *DB) ProfileForKey(ctx context.Context, presented string) (auth.Profile, bool, error) {
	prefix, ok := auth.PrefixOf(presented)
	if !ok {
		return auth.Profile{}, false, nil
	}

	var (
		p                 auth.Profile
		hash              []byte
		lastUsed, revoked sql.NullTime
	)
	err := d.db.QueryRowContext(ctx, `
		SELECT id, prefix, key_hash, name, role, note, created_at, last_used_at, revoked_at
		FROM api_keys WHERE prefix = $1 AND revoked_at IS NULL`, prefix,
	).Scan(&p.ID, &p.Prefix, &hash, &p.Name, &p.Role, &p.Note, &p.CreatedAt, &lastUsed, &revoked)
	if err == sql.ErrNoRows {
		return auth.Profile{}, false, nil
	}
	if err != nil {
		return auth.Profile{}, false, fmt.Errorf("profile for key: %w", err)
	}
	if !auth.Matches(presented, hash) {
		return auth.Profile{}, false, nil
	}

	p.CreatedAt = p.CreatedAt.UTC()
	if lastUsed.Valid {
		t := lastUsed.Time.UTC()
		p.LastUsedAt = &t
	}
	return p, true, nil
}

// ProfileByPrefix resolves a key's public prefix to its profile.
//
// Separate from ProfileForKey, which takes the whole secret and verifies a
// digest. This one takes only the public half and verifies nothing, so it is
// exclusively for callers that have already proved possession by other means
// — specifically a session cookie, whose HMAC signature is the proof and
// whose payload is the prefix.
//
// Conflating the two is a real mistake and was made here: the cookie path
// called ProfileForKey with a prefix, which hashed the prefix, compared it
// against the digest of a full key, and failed every time. Sign-in returned
// 200, set a valid cookie, and left the browser reporting itself signed out.
func (d *DB) ProfileByPrefix(ctx context.Context, prefix string) (auth.Profile, bool, error) {
	var (
		p                 auth.Profile
		lastUsed, revoked sql.NullTime
	)
	err := d.db.QueryRowContext(ctx, `
		SELECT id, prefix, name, role, note, created_at, last_used_at, revoked_at
		FROM api_keys WHERE prefix = $1 AND revoked_at IS NULL`, prefix,
	).Scan(&p.ID, &p.Prefix, &p.Name, &p.Role, &p.Note, &p.CreatedAt, &lastUsed, &revoked)
	if err == sql.ErrNoRows {
		return auth.Profile{}, false, nil
	}
	if err != nil {
		return auth.Profile{}, false, fmt.Errorf("profile by prefix: %w", err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	if lastUsed.Valid {
		t := lastUsed.Time.UTC()
		p.LastUsedAt = &t
	}
	return p, true, nil
}

// TouchKey records that a key was used.
//
// Deliberately coarse: the column exists to answer "is anyone still using
// this" before revoking it, and writing a row on every single request to
// improve a timestamp nobody reads to the second is a poor trade.
func (d *DB) TouchKey(ctx context.Context, id int64, at time.Time) error {
	// The cast on the second parameter is required, not decorative.
	// Postgres cannot infer a type for a bare parameter on the left of an
	// interval subtraction, so without it the statement fails to plan and
	// every update silently errored — the column read "never used" for keys
	// that had just authenticated a request.
	_, err := d.db.ExecContext(ctx, `
		UPDATE api_keys SET last_used_at = $2::timestamptz
		WHERE id = $1
		  AND (last_used_at IS NULL
		       OR last_used_at < $2::timestamptz - interval '5 minutes')`,
		id, at.UTC())
	if err != nil {
		return fmt.Errorf("touch key: %w", err)
	}
	return nil
}

// ListKeys returns every profile, revoked ones included.
func (d *DB) ListKeys(ctx context.Context) ([]auth.Profile, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, prefix, name, role, note, created_at, last_used_at, revoked_at
		FROM api_keys ORDER BY revoked_at IS NOT NULL, created_at`)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()

	var out []auth.Profile
	for rows.Next() {
		var p auth.Profile
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&p.ID, &p.Prefix, &p.Name, &p.Role, &p.Note,
			&p.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("list keys: scan: %w", err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		if lastUsed.Valid {
			t := lastUsed.Time.UTC()
			p.LastUsedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time.UTC()
			p.RevokedAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokeKey withdraws a key by its prefix.
func (d *DB) RevokeKey(ctx context.Context, prefix string) (bool, error) {
	res, err := d.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = now() WHERE prefix = $1 AND revoked_at IS NULL`, prefix)
	if err != nil {
		return false, fmt.Errorf("revoke key: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountActiveKeys reports how many keys can currently be used.
func (d *DB) CountActiveKeys(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		`SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&n)
	return n, err
}
