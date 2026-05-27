package codingagent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wso2/asdlc/asdlc-service/clients/clustergatewayproxy"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// finalLogTailBytes caps the captured snapshot size. Agents that go
// runaway-verbose still get useful diagnostics without bloating the
// `coding_agent_logs` table. 256KiB = ~3000 lines of typical output.
const finalLogTailBytes = 256 * 1024

// JobWatcher polls per-task Job status via cluster-gateway-proxy and
// projects terminal phases onto ComponentTask rows (WS2.5).
//
// Why polling and not a K8s informer: the proxy stub doesn't carry
// watch semantics across the cluster boundary, and the agent runs
// per-task (one Job per row) on a cadence of seconds-to-minutes —
// polling is fine and matches the existing buildWatcher.
//
// What this watcher does NOT do:
//
//   - It does NOT drive "ready_for_review" or "merged" transitions.
//     Those rides the GitHub webhook path (pull_request.ready_for_review
//     → in_progress→ready_for_review; pull_request.closed merged=true →
//     dispatches the build). The Job succeeding only means the agent
//     pod exited zero — the PR is the durable artifact.
//
//   - It does NOT poll active Jobs forever. Once a task lands in a
//     terminal state (merged / rejected / failed), the Job is past its
//     usefulness and the watcher stops looking at it.
type JobWatcher struct {
	db    *gorm.DB
	proxy *clustergatewayproxy.Client

	// pollInterval gates the outer loop. Defaults to 30s; tests pin
	// shorter intervals to force faster ticks.
	pollInterval time.Duration

	// nsResolver maps a task → its expected remote-worker NS. Pulled
	// out of the watcher so unit tests can stub it. Defaults to
	// RemoteWorkerNamespace(org.UUID) at construction time.
	nsResolver func(ctx context.Context, task *models.ComponentTask) (string, bool)

	// activeStatuses are the ComponentTask statuses where the agent
	// Job is still meaningful. Listed in models.TaskStatus.* terms.
	// Anything else is terminal.
	activeStatuses []string

	once sync.Once
}

// NewJobWatcher constructs a watcher. db + proxy required; nil panics
// at boot rather than silently no-op'ing.
func NewJobWatcher(db *gorm.DB, proxy *clustergatewayproxy.Client) *JobWatcher {
	if db == nil || proxy == nil {
		panic("codingagent.JobWatcher: db + proxy are required")
	}
	return &JobWatcher{
		db:           db,
		proxy:        proxy,
		pollInterval: 30 * time.Second,
		activeStatuses: []string{
			string(models.TaskStatusInProgress),
		},
	}
}

// WithPollInterval overrides the default 30s loop cadence.
func (w *JobWatcher) WithPollInterval(d time.Duration) *JobWatcher {
	if d > 0 {
		w.pollInterval = d
	}
	return w
}

// WithNamespaceResolver overrides the NS lookup. Tests use this to
// avoid hitting the Organization table.
func (w *JobWatcher) WithNamespaceResolver(fn func(context.Context, *models.ComponentTask) (string, bool)) *JobWatcher {
	w.nsResolver = fn
	return w
}

// Run blocks until ctx is canceled. Each tick scans `component_tasks`
// for rows with last_coding_agent_run_name set + status still active,
// looks up the Job via the proxy, and updates the row when the Job is
// terminal.
func (w *JobWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.once.Do(func() {
		slog.Info("codingagent.JobWatcher: started", "interval", w.pollInterval)
	})

	// Tick once immediately so first sweep doesn't wait pollInterval.
	w.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("codingagent.JobWatcher: stopping")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *JobWatcher) tick(ctx context.Context) {
	var tasks []models.ComponentTask
	if err := w.db.WithContext(ctx).
		Where("last_coding_agent_run_name <> ''").
		Where("status IN ?", w.activeStatuses).
		Find(&tasks).Error; err != nil {
		slog.ErrorContext(ctx, "codingagent.JobWatcher: list tasks failed", "error", err)
		return
	}
	for i := range tasks {
		w.checkOne(ctx, &tasks[i])
	}
}

func (w *JobWatcher) checkOne(ctx context.Context, task *models.ComponentTask) {
	ns, ok := w.resolveNS(ctx, task)
	if !ok {
		slog.DebugContext(ctx, "codingagent.JobWatcher: NS resolve failed; skip",
			"task", task.ID, "ocOrgId", task.OrgID)
		return
	}
	status, err := w.proxy.GetJob(ctx, ns, task.LastCodingAgentRunName)
	if err != nil {
		if errors.Is(err, clustergatewayproxy.ErrNotFound) {
			// Job vanished — TTL'd or operator-deleted. Mark failed so
			// the row doesn't loop forever.
			w.markFailed(ctx, task, "job_not_found_in_namespace")
			return
		}
		slog.WarnContext(ctx, "codingagent.JobWatcher: GetJob failed",
			"task", task.ID, "ns", ns, "run", task.LastCodingAgentRunName, "error", err)
		return
	}

	// Only act on terminal conditions. While the Job is running we
	// rely on the GitHub-webhook path to advance the task — the agent
	// pushing a commit + flipping the PR ready_for_review is the
	// durable signal, not Job.status.active.
	switch {
	case status.Succeeded > 0:
		// Agent reported success but the GitHub webhook hasn't moved
		// the task forward yet. Don't transition here — wait for
		// pull_request.ready_for_review or push. This branch exists so
		// the watcher logs the successful run name for tracing.
		slog.InfoContext(ctx, "codingagent.JobWatcher: agent succeeded; awaiting GitHub webhook",
			"task", task.ID, "ns", ns, "run", task.LastCodingAgentRunName)
		w.captureFinalLog(ctx, task, ns, "Succeeded")
		w.cleanupPerRunExternalSecrets(ctx, task, ns)
	case status.Failed > 0:
		reason := "job_failed"
		if len(status.Conditions) > 0 {
			for _, c := range status.Conditions {
				if c.Type == "Failed" && c.Status == "True" {
					reason = "job_failed:" + c.Reason
					break
				}
			}
		}
		w.captureFinalLog(ctx, task, ns, "Failed")
		w.cleanupPerRunExternalSecrets(ctx, task, ns)
		w.markFailed(ctx, task, reason)
	}
}

// cleanupPerRunExternalSecrets deletes the per-run ExternalSecrets
// applied by codingagent.Dispatcher (anthropic + github, plus the
// optional WS2.4 runner-auth). Owner-ref-based GC isn't possible
// because the Job UID doesn't exist when the ESes are created
// (chicken-and-egg: the Job needs the materialized Secrets to start),
// so the watcher does explicit cleanup on terminal phase. Best-effort:
// 404 is success, other failures are logged but never block status
// projection. Without this, every dispatch leaks 2+ ES objects per
// per-org NS until manual cleanup.
func (w *JobWatcher) cleanupPerRunExternalSecrets(ctx context.Context, task *models.ComponentTask, ns string) {
	if w.proxy == nil || task.LastCodingAgentRunName == "" {
		return
	}
	names := []string{
		task.LastCodingAgentRunName + "-anthropic-es",
		task.LastCodingAgentRunName + "-github-es",
		task.LastCodingAgentRunName + "-runner-auth-es", // WS2.4 — no-op when absent (404 tolerated)
	}
	for _, name := range names {
		if err := w.proxy.DeleteExternalSecret(ctx, ns, name); err != nil {
			slog.WarnContext(ctx, "codingagent.JobWatcher: cleanup ExternalSecret failed",
				"task", task.ID, "ns", ns, "es", name, "error", err)
		}
	}
}

// captureFinalLog reads the agent pod's stdout/stderr once and
// persists it to coding_agent_logs. Idempotent on (task_id, run_name):
// a retried capture (e.g. watcher restart mid-flight) re-upserts the
// row without erroring. Best-effort — failures are logged but never
// block the watcher's primary job (status projection).
func (w *JobWatcher) captureFinalLog(ctx context.Context, task *models.ComponentTask, ns, phase string) {
	if w.db == nil || w.proxy == nil {
		return
	}
	// Skip if we already captured this run.
	var exists int64
	if err := w.db.WithContext(ctx).
		Model(&models.CodingAgentLog{}).
		Where("task_id = ? AND run_name = ?", task.ID, task.LastCodingAgentRunName).
		Count(&exists).Error; err == nil && exists > 0 {
		return
	}

	podName, err := w.proxy.GetJobPodName(ctx, ns, task.LastCodingAgentRunName)
	if err != nil {
		slog.WarnContext(ctx, "codingagent.JobWatcher: captureFinalLog: pod lookup failed",
			"task", task.ID, "ns", ns, "run", task.LastCodingAgentRunName, "error", err)
		return
	}
	body, err := w.proxy.TailPodLog(ctx, ns, podName, clustergatewayproxy.PodLogOptions{
		Timestamps: true,
		LimitBytes: finalLogTailBytes,
	})
	if err != nil {
		slog.WarnContext(ctx, "codingagent.JobWatcher: captureFinalLog: tail failed",
			"task", task.ID, "ns", ns, "pod", podName, "error", err)
		return
	}
	taskUUID, err := uuid.Parse(task.ID)
	if err != nil {
		slog.WarnContext(ctx, "codingagent.JobWatcher: captureFinalLog: invalid task id", "task", task.ID, "error", err)
		return
	}
	row := models.CodingAgentLog{
		TaskID:     taskUUID,
		RunName:    task.LastCodingAgentRunName,
		FinalPhase: phase,
		LogText:    string(body),
		SizeBytes:  int64(len(body)),
	}
	if err := w.db.WithContext(ctx).Create(&row).Error; err != nil {
		slog.WarnContext(ctx, "codingagent.JobWatcher: captureFinalLog: persist failed",
			"task", task.ID, "run", task.LastCodingAgentRunName, "error", err)
		return
	}
	slog.InfoContext(ctx, "codingagent.JobWatcher: captured final log",
		"task", task.ID, "run", task.LastCodingAgentRunName, "phase", phase, "bytes", row.SizeBytes)
}

func (w *JobWatcher) resolveNS(ctx context.Context, task *models.ComponentTask) (string, bool) {
	if w.nsResolver != nil {
		return w.nsResolver(ctx, task)
	}
	var org models.Organization
	// `organizations.name` is the OC org slug (== `task.OrgID`).
	if err := w.db.WithContext(ctx).Where("name = ?", task.OrgID).First(&org).Error; err != nil {
		return "", false
	}
	// Prefer the Thunder-issued ouId — that's what SM-API derives the
	// NS from. Fall back to the local PK only when the row predates
	// the orgensure lazy-fill (NS will likely mismatch in that case
	// but better than returning empty).
	var uid string
	if org.ThunderOrgUUID != nil && *org.ThunderOrgUUID != uuid.Nil {
		uid = org.ThunderOrgUUID.String()
	} else {
		uid = org.UUID.String()
	}
	if uid == "" || uid == "00000000-0000-0000-0000-000000000000" {
		return "", false
	}
	return RemoteWorkerNamespace(uid), true
}

func (w *JobWatcher) markFailed(ctx context.Context, task *models.ComponentTask, reason string) {
	if err := w.db.WithContext(ctx).
		Model(&models.ComponentTask{}).
		Where("id = ?", task.ID).
		Updates(map[string]any{
			"status":        string(models.TaskStatusFailed),
			"error_message": reason,
		}).Error; err != nil {
		slog.ErrorContext(ctx, "codingagent.JobWatcher: markFailed update failed",
			"task", task.ID, "reason", reason, "error", err)
		return
	}
	slog.InfoContext(ctx, "codingagent.JobWatcher: task marked failed",
		"task", task.ID, "reason", reason)
}
