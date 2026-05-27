package credentials

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// Validator probes every active org_credentials row on a fixed interval
// (default 24h) and runs whatever drift / revocation reconciliation the
// row's kind requires. Per phase2.md §6.10:
//
//   - app-installation: GET /app/installations/{installationId}.
//     200 → refresh account.login if changed (rename drift). Update
//     last_validated_at. 401/404/410 → trigger disconnect cascade.
//   - user-pat: GET /user with the cached PAT. 200 with new login →
//     identity drift (PrevIdentityLogin/IdentityChangedAt). 401 →
//     trigger disconnect cascade. Otherwise update last_validated_at.
//
// Single-flight per process across replicas via
// pg_advisory_xact_lock(hashtext('validator')); rows are listed inside
// the lock and walked outside it (so the lock holder doesn't block the
// next-tick election while making slow GitHub calls).
//
// The disconnect cascade itself runs on the BFF (where ComponentTask
// lives), not in git-service. The validator calls back via the
// CascadeTrigger callback — wired in cmd/git-service/main.go to the
// asdlcservice client's TriggerDisconnect.
type Validator struct {
	db       *gorm.DB
	probes   ValidatorProbes
	cascade  CascadeTrigger
	interval time.Duration
	now      func() time.Time

	// metrics — updated atomically from RunOnce; read by the test-tick
	// endpoint to surface a summary in the response body.
	lastValidated atomic.Int64
	lastDrifted   atomic.Int64
	lastCascaded  atomic.Int64
}

// ValidatorProbes is the seam between the validator's loop and GitHub /
// the database mutations. The validator never builds Authorization headers
// or runs raw HTTP — that's all behind these methods (which the
// production wiring backs with the GitHubClient + CredentialService, and
// tests back with fakes).
type ValidatorProbes interface {
	// ListActiveRows returns the rows to probe. The validator iterates
	// the slice without holding the validator lock.
	ListActiveRows(ctx context.Context) ([]ActiveRow, error)

	// ProbePAT performs GET /user with the row's stored PAT (resolved via
	// the resolver). On 200 returns identity{login, name, email}; on 401
	// returns ErrCredentialUnauthorized; on 5xx returns ErrCredentialTransient.
	ProbePAT(ctx context.Context, row ActiveRow) (login, name, email string, err error)

	// ProbeApp calls GET /app/installations/{installationId} via the
	// AppTokenMinter's signed App JWT. On 200 returns accountLogin; on
	// 401/404/410 returns ErrCredentialUnauthorized; on 5xx returns
	// ErrCredentialTransient.
	ProbeApp(ctx context.Context, row ActiveRow) (accountLogin string, err error)

	// RecordIdentityFromGitHub commits identity / drift columns under the
	// row's transaction. drifted=true when the new login differed from the
	// stored identity_login.
	RecordIdentityFromGitHub(ctx context.Context, ocOrgID, login, name, email string) (drifted bool, err error)

	// UpdateGitHubLogin updates the github_login column for App-mode rename drift.
	UpdateGitHubLogin(ctx context.Context, ocOrgID, login string) error

	// TouchValidatedAt updates last_validated_at without identity changes.
	TouchValidatedAt(ctx context.Context, ocOrgID string) error
}

// ActiveRow is the projection the validator walks. Avoids a GORM model
// dependency so the validator package stays free of DB schema details.
type ActiveRow struct {
	OcOrgID        string
	Kind           string
	GitHubLogin    string
	IdentityLogin  string
	InstallationID *int64
	Status         string
}

// CascadeTrigger is invoked by the validator on a confirmed unauthorized
// signal (401 from PAT mode; 401/404/410 from App mode). The callback
// runs the BFF-side disconnect cascade per §6.7. Errors are logged but
// don't stop the validator's iteration.
type CascadeTrigger func(ctx context.Context, ocOrgID, cause string) error

// Common validator errors. Probes return these so the loop can branch.
var (
	ErrCredentialUnauthorized = errors.New("credential: unauthorized")
	ErrCredentialTransient    = errors.New("credential: transient github error")
)

// RunSummary is the result of a single validation pass — returned by
// RunOnce and emitted by the periodic Run loop on each tick.
type RunSummary struct {
	ValidatedRows int   `json:"validatedRows"`
	DriftedRows   int   `json:"identityDriftRows"`
	CascadedRows  int   `json:"disconnectedRows"`
	StartedAt     int64 `json:"startedAtUnix"`
}

// NewValidator constructs the validator. interval may be zero in which
// case Run uses 24h. cascade may be nil; in that case the unauthorised
// path logs a warning instead of triggering a cascade (used only in
// tests to assert classification without exercising the BFF callback).
func NewValidator(db *gorm.DB, probes ValidatorProbes, cascade CascadeTrigger, interval time.Duration) *Validator {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Validator{
		db:       db,
		probes:   probes,
		cascade:  cascade,
		interval: interval,
		now:      time.Now,
	}
}

// Run blocks on the validator ticker until ctx is cancelled. Idempotent
// across replicas via a Postgres advisory lock — only the replica that
// wins the lock for a given tick performs the probe.
func (v *Validator) Run(ctx context.Context) {
	t := time.NewTicker(v.interval)
	defer t.Stop()

	// Run once immediately so a fresh deploy doesn't wait 24h for the
	// first probe. The advisory lock makes this safe with multi-replica.
	if _, err := v.RunOnce(ctx); err != nil {
		slog.WarnContext(ctx, "credential validator first-run failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := v.RunOnce(ctx); err != nil {
				slog.WarnContext(ctx, "credential validator tick failed", "error", err)
			}
		}
	}
}

// RunOnce performs one validation pass synchronously and returns the
// summary. Used by both the Run loop and the test-tick endpoint.
func (v *Validator) RunOnce(ctx context.Context) (*RunSummary, error) {
	summary := &RunSummary{StartedAt: v.now().Unix()}

	// Single-flight election. The validator-scoped advisory lock is
	// transaction-scoped; we list rows inside it then release before
	// iterating. A second replica calling RunOnce concurrently sees the
	// lock held, skips, and returns an empty summary — no double-validation.
	var rows []ActiveRow
	acquired, err := v.electAndList(ctx, &rows)
	if err != nil {
		return nil, err
	}
	if !acquired {
		slog.DebugContext(ctx, "credential validator: another replica holds the lock; skipping tick")
		return summary, nil
	}

	for i := range rows {
		row := rows[i]
		if err := v.processRow(ctx, row, summary); err != nil {
			slog.ErrorContext(ctx, "credential validator: row failed",
				"ocOrgId", row.OcOrgID, "kind", row.Kind, "error", err)
		}
	}
	v.lastValidated.Store(int64(summary.ValidatedRows))
	v.lastDrifted.Store(int64(summary.DriftedRows))
	v.lastCascaded.Store(int64(summary.CascadedRows))
	return summary, nil
}

// processRow handles one row. Errors returned here are logged but don't
// abort the pass — one row's transient failure doesn't affect siblings.
func (v *Validator) processRow(ctx context.Context, row ActiveRow, summary *RunSummary) error {
	switch row.Kind {
	case "user-pat":
		login, name, email, err := v.probes.ProbePAT(ctx, row)
		if errors.Is(err, ErrCredentialUnauthorized) {
			summary.CascadedRows++
			return v.fireCascade(ctx, row.OcOrgID, "validator.unauthorized")
		}
		if err != nil {
			return err
		}
		drifted, err := v.probes.RecordIdentityFromGitHub(ctx, row.OcOrgID, login, name, email)
		if err != nil {
			return err
		}
		if drifted {
			summary.DriftedRows++
		}
		summary.ValidatedRows++
		return nil

	case "app-installation":
		accountLogin, err := v.probes.ProbeApp(ctx, row)
		if errors.Is(err, ErrCredentialUnauthorized) {
			summary.CascadedRows++
			return v.fireCascade(ctx, row.OcOrgID, "validator.unauthorized")
		}
		if err != nil {
			return err
		}
		// App-mode "drift" is the install's account.login changing
		// (org rename on GitHub). Update github_login + last_validated_at.
		// No identity_login update — the App's bot identity is fixed by
		// the App definition.
		if accountLogin != "" && accountLogin != row.GitHubLogin {
			if err := v.probes.UpdateGitHubLogin(ctx, row.OcOrgID, accountLogin); err != nil {
				return err
			}
			summary.DriftedRows++
		}
		if err := v.probes.TouchValidatedAt(ctx, row.OcOrgID); err != nil {
			return err
		}
		summary.ValidatedRows++
		return nil

	default:
		// Unknown kind. Log and skip — adding a new kind requires
		// extending the validator deliberately, not silently dropping
		// the row from the validation budget.
		slog.WarnContext(ctx, "credential validator: unknown kind", "ocOrgId", row.OcOrgID, "kind", row.Kind)
		return nil
	}
}

func (v *Validator) fireCascade(ctx context.Context, ocOrgID, cause string) error {
	if v.cascade == nil {
		slog.WarnContext(ctx, "credential validator: cascade trigger not configured; logging only",
			"ocOrgId", ocOrgID, "cause", cause)
		return nil
	}
	if err := v.cascade(ctx, ocOrgID, cause); err != nil {
		return err
	}
	slog.InfoContext(ctx, "credential validator: triggered disconnect cascade",
		"ocOrgId", ocOrgID, "cause", cause)
	return nil
}

// electAndList runs the lock acquisition and row snapshot in a single
// transaction. *rows is populated only when the lock was actually
// acquired (returning acquired=true).
func (v *Validator) electAndList(ctx context.Context, rows *[]ActiveRow) (bool, error) {
	acquired := false
	err := v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// pg_try_advisory_xact_lock returns true if the lock was
		// acquired, false otherwise. The lock is automatically released
		// when the transaction commits, which happens immediately after
		// listing rows.
		var got struct{ Got bool }
		if err := tx.Raw(`SELECT pg_try_advisory_xact_lock(?) AS got`, validatorLockKey).Scan(&got).Error; err != nil {
			return err
		}
		if !got.Got {
			return nil
		}
		acquired = true
		listed, err := v.probes.ListActiveRows(ctx)
		if err != nil {
			return err
		}
		*rows = listed
		return nil
	})
	if err != nil {
		return false, err
	}
	return acquired, nil
}

// validatorLockKey is the int64 hashed from "validator". Computed once
// at package init for clarity at the call site. Same FNV scheme as the
// projector's lock keys.
const validatorLockKey int64 = 0x76616c696461746f // "valid_to_" prefix bytes — stable; collision space is per-database, single key.
