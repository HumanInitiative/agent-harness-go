package csr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("csr: lock is held by another process")

// ErrLockLost means a lock expired and was taken over before it could be
// renewed; the work it protected must stop.
var ErrLockLost = errors.New("csr: lock was lost")

// LockHeldError describes who holds a lock. It matches ErrLocked.
type LockHeldError struct {
	Name       string
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time
}

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("csr: lock %q is held by %s since %s (expires %s unless renewed)",
		e.Name, e.Holder, e.AcquiredAt.UTC().Format(time.RFC3339), e.ExpiresAt.UTC().Format(time.RFC3339))
}

func (e *LockHeldError) Is(target error) bool { return target == ErrLocked }

// lockTS formats lock times with a fixed width so SQLite can compare them
// as text (RFC 3339 with trimmed fractions does not sort correctly).
func lockTS(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// AcquireLock takes the named lock for holder until ttl from now, or
// returns a *LockHeldError. An expired lock (its holder crashed or hung) is
// taken over. Acquiring is a single statement, so two processes racing for
// the lock cannot both win.
func (s *Store) AcquireLock(ctx context.Context, name, holder string, ttl time.Duration) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO locks (name, holder, acquired_at, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET holder = excluded.holder, acquired_at = excluded.acquired_at,
			expires_at = excluded.expires_at
		WHERE locks.expires_at <= ?`,
		name, holder, lockTS(now), lockTS(now.Add(ttl)), lockTS(now))
	if err != nil {
		return fmt.Errorf("csr: acquire lock %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	held := &LockHeldError{Name: name}
	var acquired, expires string
	err = s.db.QueryRowContext(ctx, `SELECT holder, acquired_at, expires_at FROM locks WHERE name = ?`, name).
		Scan(&held.Holder, &acquired, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return s.AcquireLock(ctx, name, holder, ttl) // released in between
	}
	if err != nil {
		return err
	}
	held.AcquiredAt = parseTS(sql.NullString{String: acquired, Valid: true})
	held.ExpiresAt = parseTS(sql.NullString{String: expires, Valid: true})
	return held
}

// RenewLock extends a lock holder still holds; ErrLockLost when it does
// not.
func (s *Store) RenewLock(ctx context.Context, name, holder string, ttl time.Duration) error {
	res, err := s.db.ExecContext(ctx, `UPDATE locks SET expires_at = ? WHERE name = ? AND holder = ?`,
		lockTS(s.now().Add(ttl)), name, holder)
	if err != nil {
		return fmt.Errorf("csr: renew lock %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLockLost
	}
	return nil
}

// ReleaseLock gives up a lock holder holds. Releasing a lock held by
// someone else does nothing.
func (s *Store) ReleaseLock(ctx context.Context, name, holder string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM locks WHERE name = ? AND holder = ?`, name, holder)
	return err
}
