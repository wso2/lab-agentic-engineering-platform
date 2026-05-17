package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"

	"gorm.io/gorm"
)

// RequirementsDirLockBusy is returned when another writer holds the lock
// and the caller asked for a non-blocking acquire. Controllers map it to
// HTTP 409 with `code: "chat_in_progress"` so the UI can render the
// "another writer is editing" toast.
var RequirementsDirLockBusy = errors.New("requirements dir lock busy")

// RequirementsDirLocker hands out per-project advisory locks on the
// requirements directory. Two flavours:
//
//   - WithTxLock: short-lived writers (PUT, DELETE, save, discard, generate)
//     wrap their work in a transaction and use pg_advisory_xact_lock. Lock
//     auto-releases on COMMIT/ROLLBACK — same idiom as
//     services/webhook/projector.go.
//
//   - AcquireSession: long-lived writers (chat SSE stream) pin a dedicated
//     *sql.Conn out of the GORM pool, call pg_advisory_lock, and explicitly
//     release on stream close. Necessary because we can't hold a single
//     transaction open across many model-wait windows (PgBouncer in
//     transaction mode would force-close it; autovacuum gets blocked).
//
// Both flavours share the same lock key, so contention is correctly
// serialised across the two paths.
type RequirementsDirLocker struct {
	db *gorm.DB
}

func NewRequirementsDirLocker(db *gorm.DB) *RequirementsDirLocker {
	return &RequirementsDirLocker{db: db}
}

// lockKey returns the Postgres advisory-lock key as a signed 64-bit int.
// Postgres advisory locks use bigint; we hash the (orgID, projectID) pair
// through FNV-64 to fit. Collisions are tolerable — at worst two unrelated
// projects serialise needlessly.
func lockKey(orgID, projectID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("reqdir:"))
	_, _ = h.Write([]byte(orgID))
	_, _ = h.Write([]byte("/"))
	_, _ = h.Write([]byte(projectID))
	// Reinterpret the unsigned hash as signed int64 — Postgres accepts the
	// full int64 range.
	return int64(h.Sum64()) //nolint:gosec
}

// WithTxLock acquires a transaction-scoped advisory lock for the given
// (orgID, projectID) inside its own transaction, runs `fn`, and returns
// fn's error. The lock auto-releases when the transaction ends.
//
// Returns RequirementsDirLockBusy if the lock is already held (we use
// pg_try_advisory_xact_lock so we never block in this short-writer path —
// the caller can retry with backoff or surface a 409 to the client).
func (l *RequirementsDirLocker) WithTxLock(ctx context.Context, orgID, projectID string, fn func(ctx context.Context) error) error {
	if l == nil || l.db == nil {
		// Locker disabled (no DB wired) — degrade to running fn without a lock.
		// This is the local-dev / test path where the lock guarantee is
		// nice-to-have.
		return fn(ctx)
	}
	key := lockKey(orgID, projectID)
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var got bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", key).Scan(&got).Error; err != nil {
			return fmt.Errorf("acquire xact lock: %w", err)
		}
		if !got {
			return RequirementsDirLockBusy
		}
		return fn(ctx)
	})
}

// SessionLock is the return value of AcquireSession. Release() MUST be
// called (typically via defer) when the long-lived caller is done. After
// Release, the pinned connection returns to the pool.
type SessionLock struct {
	conn *sql.Conn
	key  int64
}

// Release unlocks and returns the connection to the pool. Safe to call
// multiple times.
func (s *SessionLock) Release(ctx context.Context) {
	if s == nil || s.conn == nil {
		return
	}
	defer func() {
		_ = s.conn.Close()
		s.conn = nil
	}()
	// pg_advisory_unlock returns bool; we don't care about its value — if
	// the lock was already gone the unlock is a no-op.
	if _, err := s.conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", s.key); err != nil {
		slog.WarnContext(ctx, "pg_advisory_unlock failed", "key", s.key, "error", err)
	}
}

// AcquireSession pins a dedicated connection and acquires a session-scoped
// advisory lock on it. Non-blocking: returns RequirementsDirLockBusy if
// the lock is already held by another connection.
func (l *RequirementsDirLocker) AcquireSession(ctx context.Context, orgID, projectID string) (*SessionLock, error) {
	if l == nil || l.db == nil {
		// Locker disabled — return a no-op handle so callers can defer
		// Release() unconditionally.
		return &SessionLock{}, nil
	}
	sqlDB, err := l.db.DB()
	if err != nil {
		return nil, fmt.Errorf("get raw db: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout conn: %w", err)
	}
	key := lockKey(orgID, projectID)
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire session lock: %w", err)
	}
	if !got {
		_ = conn.Close()
		return nil, RequirementsDirLockBusy
	}
	return &SessionLock{conn: conn, key: key}, nil
}
