package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// ghCreateConcurrency caps in-flight GitHub issue creations during the
// plan→detail handoff. 3 stays well below GitHub's secondary content-creation
// rate limit (per design §13a).
const ghCreateConcurrency = 3

// StreamGenerateTasks runs the two-phase tech-lead orchestration.
// Per design §3:
//
//	T1  Acquire pg_advisory_xact_lock(hashKey('techlead:'||projectID))
//	T2  Read spec / design / existing tasks; compute baseline + diffs.
//	T3  POST /v1/agents/tech-lead/plan; proxy data-plan-item to console.
//	T4  Persist N task rows; create GH issues in parallel (p-limit 3).
//	T5  POST /v1/agents/tech-lead/detail; proxy body deltas; async edit GH.
//	T6  Reconciliation pass; emit data-task-rejected for removed components.
//	T7  Emit data-finish.
//
// The console always sees one continuous SSE stream: agents-service is
// called twice in sequence behind this orchestrator.
func (s *taskService) StreamGenerateTasks(ctx context.Context, orgID, projectID string, out io.Writer, flush func()) (err error) {
	w := newSseWriter(out, flush)

	// Defensive: a panic anywhere in the orchestration must surface to the
	// console as a data-error frame, not silently kill the SSE stream. The
	// router's RecovererOnPanic middleware would otherwise swallow it after
	// headers were already written, leaving the UI stuck on "No tasks yet."
	// with no error indication.
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "tech-lead orchestration panic", "recovered", r)
			w.send("error", map[string]any{
				"scope":     "plan",
				"errorText": fmt.Sprintf("internal error: %v", r),
			})
			err = fmt.Errorf("orchestration panic: %v", r)
		}
	}()

	// T1: per-project advisory lock. Uses a transaction so the lock is
	// transaction-scoped and released on commit/rollback. The whole
	// orchestration runs inside this transaction's lifetime to keep the
	// invariant simple. We use the BLOCKING variant
	// (pg_advisory_xact_lock) — concurrent generation requests for the
	// same project should serialise, not error out. Returns void, so
	// .Exec() is the right call (no row to scan).
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("begin tx: %w", tx.Error)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()
	if lockErr := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, hashTechLeadKey(projectID)).Error; lockErr != nil {
		return fmt.Errorf("acquire lock: %w", lockErr)
	}

	// T2: load inputs. Requirements are now a multi-file bundle; concatenate
	// every doc as one corpus so the tech-lead agent sees the same content
	// regardless of how the project organised its requirement files.
	reqFiles, err := s.store.ListRequirements(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("list requirements: %w", err)
	}
	if len(reqFiles) == 0 {
		return ErrSpecNotFound
	}
	specContent := concatRequirementBundle(reqFiles)
	if specContent == "" {
		return ErrSpecNotFound
	}
	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return ErrDesignNotFound
		}
		return fmt.Errorf("read design: %w", err)
	}
	if design == nil {
		return ErrDesignNotFound
	}

	currentSpecVersion, currentDesignVersion := s.currentArtifactVersions(ctx, orgID, projectID)

	allTasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}

	// Idempotency guard: if non-rejected tasks already exist for the current
	// (spec, design) version pair, generation is a no-op. The console's
	// "Generate Tasks" button is always visible (including after iteration
	// 1) so the user may click it on a project that is already up to date;
	// this is what makes that safe — when nothing's changed, the stream
	// returns data-finish immediately without touching the agent. When the
	// design or spec has been re-tagged, no task will match, and full
	// Phase 1 + Phase 2 runs.
	freshCount := 0
	for _, t := range allTasks {
		switch t.Status {
		case string(models.TaskStatusRejected),
			string(models.TaskStatusFailed),
			string(models.TaskStatusAbandoned):
			continue
		}
		if t.SourceSpecVersion == currentSpecVersion && t.SourceDesignVersion == currentDesignVersion {
			freshCount++
		}
	}
	if freshCount > 0 {
		slog.InfoContext(ctx, "tasks already exist for current versions; emitting data-finish",
			"project", projectID, "specVersion", currentSpecVersion,
			"designVersion", currentDesignVersion, "taskCount", freshCount)
		w.send("data-finish", map[string]any{"taskCount": freshCount})
		if err := tx.Commit().Error; err == nil {
			committed = true
		}
		return nil
	}

	existingForPrompt := filterNonRejectedForPrompt(allTasks)

	baseline, err := loadBaselineBatch(ctx, s.db, projectID)
	if err != nil {
		return fmt.Errorf("baseline: %w", err)
	}

	mode := "fresh"
	var prevDesign *DesignFile
	prevSpec := ""
	if baseline != nil {
		mode = "incremental"
		// Best-effort: pull the prior design + spec at the baseline tag. Diff
		// computers tolerate empty/nil prevs.
		if s.gitClient != nil && baseline.SourceDesignVersion != "" {
			if files, err := s.gitClient.GetDesignAtTag(ctx, orgID, projectID, baseline.SourceDesignVersion); err == nil {
				prevDesign, _ = AssembleDesign(files)
			}
		}
		if s.gitClient != nil && baseline.SourceSpecVersion != "" {
			// Pull every requirement file at the baseline tag and concatenate.
			// The tag is a `v<N>` requirements tag; missing files are tolerated.
			if files, err := s.gitClient.GetRequirementsAtTag(ctx, orgID, projectID, baseline.SourceSpecVersion); err == nil {
				prevSpec = concatRequirementBundle(files)
			}
		}
	}

	var prevComponents []models.DesignComponent
	if prevDesign != nil {
		prevComponents = prevDesign.Components
	}
	designDiff := computeDesignDiff(prevComponents, design.Components)
	specDiff := computeSpecDiff(prevSpec, specContent)

	// T3: open Phase 1.
	attachedDescs, resolvedSkills := s.resolveProjectSkills(ctx, orgID, design)
	planReq := buildPlanRequest(projectID, specContent, design.Components, designDiff, specDiff, existingForPrompt, mode, attachedDescs)
	planUpstream, err := s.agentsClient.StreamTechLeadPlan(ctx, orgID, planReq)
	if err != nil {
		return fmt.Errorf("plan upstream: %w", err)
	}
	defer planUpstream.Close()

	planItems, planErr := proxyPlanStream(ctx, planUpstream, w)
	if planErr != nil {
		return planErr
	}
	if len(planItems) == 0 {
		// Validator allowed empty plan (incremental + trivial diff).
		// Run reconciliation, emit data-finish, return.
		batchID := uuid.NewString()
		if err := s.runReconciliationStreamed(ctx, orgID, projectID, design, w); err != nil {
			slog.WarnContext(ctx, "reconciliation pass error", "error", err)
		}
		w.send("data-finish", map[string]any{"batchId": batchID, "taskCount": 0})
		if err := tx.Commit().Error; err == nil {
			committed = true
		}
		return nil
	}

	// T4: persist + create GH issues.
	batchID := uuid.NewString()
	repoURL, repoSlug := s.repoInfoForBody(ctx, orgID, projectID)
	persisted, err := s.persistAndIssue(ctx, w, orgID, projectID, batchID, currentSpecVersion, currentDesignVersion, planItems, design, repoURL, repoSlug)
	if err != nil {
		return err
	}

	// T5: open Phase 2 over the surviving (issued) tasks.
	survived := surviving(persisted)
	slog.InfoContext(ctx, "tech-lead T5: phase 2 trigger",
		"persisted", len(persisted), "survived", len(survived))
	if len(survived) > 0 {
		s.runPhase2(ctx, w, orgID, projectID, specContent, survived, design, allTasks, repoURL, repoSlug, resolvedSkills)
	}

	// T6: reconciliation.
	if err := s.runReconciliationStreamed(ctx, orgID, projectID, design, w); err != nil {
		slog.WarnContext(ctx, "reconciliation pass error", "error", err)
	}

	// T7: data-finish.
	w.send("data-finish", map[string]any{
		"batchId":   batchID,
		"taskCount": len(persisted),
	})

	if err := tx.Commit().Error; err == nil {
		committed = true
	}
	return nil
}

// runPhase2 opens the agents-service detail SSE and proxies body events to
// the console. Logs liberally — silent failure here is what kept the UI
// stuck on "Generating details…" with no signal during initial verification.
// Emits scope:detail errors to the SSE stream so the frontend can surface
// them to the user.
func (s *taskService) runPhase2(
	ctx context.Context,
	w *sseWriter,
	orgID, projectID, specContent string,
	survived []persistedItem,
	design *DesignFile,
	allTasks []models.ComponentTask,
	repoURL, repoSlug string,
	resolvedSkills []agents.SkillRecord,
) {
	detailReq := buildDetailRequest(projectID, specContent, survived, design, allTasks, resolvedSkills)
	slog.InfoContext(ctx, "tech-lead phase 2: opening detail stream", "items", len(detailReq.Items))

	detailUpstream, err := s.agentsClient.StreamTechLeadDetail(ctx, orgID, detailReq)
	if err != nil {
		slog.ErrorContext(ctx, "tech-lead phase 2: detail upstream open failed", "error", err)
		w.send("error", map[string]any{
			"scope":     "detail",
			"errorText": err.Error(),
		})
		return
	}
	defer detailUpstream.Close()
	slog.InfoContext(ctx, "tech-lead phase 2: detail stream opened, proxying")

	s.proxyDetailStream(ctx, detailUpstream, w, survived, repoURL, repoSlug)
	slog.InfoContext(ctx, "tech-lead phase 2: detail proxy ended")
}

// =============================================================================
// SSE writer
// =============================================================================

type sseWriter struct {
	out   io.Writer
	flush func()
	mu    sync.Mutex
}

func newSseWriter(out io.Writer, flush func()) *sseWriter {
	return &sseWriter{out: out, flush: flush}
}

func (w *sseWriter) send(event string, data any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	frame := map[string]any{"type": event}
	if data != nil {
		frame["data"] = data
	}
	b, _ := json.Marshal(frame)
	fmt.Fprintf(w.out, "data: %s\n\n", b)
	if w.flush != nil {
		w.flush()
	}
}

// passthrough streams a frame already in the wire format from agents-service
// to the console.
func (w *sseWriter) passthrough(line []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out.Write(line)
	w.out.Write([]byte("\n"))
	if w.flush != nil {
		w.flush()
	}
}

// =============================================================================
// Phase 1 — plan stream proxy
// =============================================================================

type planItemFrame struct {
	TempID        string   `json:"tempId"`
	ComponentName string   `json:"componentName"`
	Title         string   `json:"title"`
	Rationale     string   `json:"rationale"`
	DependsOn     []string `json:"dependsOn"`
}

// proxyPlanStream forwards plan-item frames to the console and collects them
// into a slice. Emits at end:
//   - planItems list and nil err: success
//   - nil + err: an upstream error frame; orchestrator returns immediately.
func proxyPlanStream(ctx context.Context, upstream io.Reader, w *sseWriter) ([]planItemFrame, error) {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var items []planItemFrame
	completed := false
	var sseErr error

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			// keep-alive comment or [DONE]
			w.passthrough(line)
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var head struct {
			Type string `json:"type"`
			Data json.RawMessage
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			w.passthrough(line)
			continue
		}
		switch head.Type {
		case "data-plan-item":
			var item planItemFrame
			if err := json.Unmarshal(head.Data, &item); err == nil {
				items = append(items, item)
			}
			w.passthrough(line)
		case "data-plan-complete":
			completed = true
			// Don't proxy this — BFF emits its own data-finish later.
		case "error":
			// Forward as-is; orchestrator surfaces as plan-scope error.
			w.passthrough(line)
			sseErr = fmt.Errorf("plan-stream error frame")
		default:
			w.passthrough(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("plan stream scan: %w", err)
	}
	if sseErr != nil {
		return nil, sseErr
	}
	if !completed && len(items) == 0 {
		return nil, fmt.Errorf("plan stream closed without completion or items")
	}
	return items, nil
}

// =============================================================================
// T4 — persistence + GH issue creation
// =============================================================================

type persistedItem struct {
	TempID    string
	Task      *models.ComponentTask
	IssueOK   bool
	IssueErr  string
	DesignSli string // marshalled design slice for Phase 2
}

func surviving(items []persistedItem) []persistedItem {
	out := make([]persistedItem, 0, len(items))
	for _, it := range items {
		if it.IssueOK {
			out = append(out, it)
		}
	}
	return out
}

func (s *taskService) persistAndIssue(
	ctx context.Context,
	w *sseWriter,
	orgID, projectID, batchID string,
	specVersion, designVersion string,
	plan []planItemFrame,
	design *DesignFile,
	repoURL, repoSlug string,
) ([]persistedItem, error) {
	// Index design components for slice extraction and DependsOn lookup.
	byName := make(map[string]models.DesignComponent, len(design.Components))
	componentNameSet := make(map[string]struct{}, len(design.Components))
	for _, c := range design.Components {
		byName[strings.ToLower(c.Name)] = c
		componentNameSet[c.Name] = struct{}{}
	}

	// Persist rows up front, sequentially — ordering by plan index.
	// F2: DependsOnComponents is pulled directly from the design (platform-
	// authored, not LLM-authored) and validated against the component set
	// at persist time. The LLM's `PlanItem.dependsOn` is now context-only.
	rows := make([]persistedItem, len(plan))
	for i, p := range plan {
		comp := byName[strings.ToLower(p.ComponentName)]
		deps := append([]string(nil), comp.DependsOn...)
		for _, dep := range deps {
			if _, ok := componentNameSet[dep]; !ok {
				return nil, fmt.Errorf(
					"design.Components[%s].dependsOn references unknown component %q (must match one of design.Components[*].name)",
					p.ComponentName, dep,
				)
			}
		}
		task := &models.ComponentTask{
			ProjectID:           projectID,
			OrgID:               orgID,
			ComponentName:       p.ComponentName,
			Title:               p.Title,
			Rationale:           p.Rationale,
			DependsOnComponents: models.StringSlice(deps),
			BatchID:             ptrString(batchID),
			SourceSpecVersion:   specVersion,
			SourceDesignVersion: designVersion,
			Order:               i + 1,
			Status:              string(models.TaskStatusPending),
			LifecycleStatus:     string(models.TaskLifecycleGhIssueWaiting),
			ExecType:            "WORKER",
		}
		if err := s.taskRepo.Create(ctx, task); err != nil {
			return nil, fmt.Errorf("create task row %d: %w", i, err)
		}
		// Strip openAPISpec — the YAML can be huge and the prompt explicitly
		// tells the model to reference the on-disk
		// `specs/design/components/<name>/openapi.yaml` rather than inline
		// it. Removing it from the slice saves tokens and removes temptation.
		compForPrompt := comp
		compForPrompt.OpenAPISpec = ""
		designSlice, _ := json.Marshal(compForPrompt)
		rows[i] = persistedItem{TempID: p.TempID, Task: task, DesignSli: string(designSlice)}
	}

	// Parallel issue creation, bounded by ghCreateConcurrency.
	sem := make(chan struct{}, ghCreateConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			row := &rows[i]
			comp := byName[strings.ToLower(row.Task.ComponentName)]
			err := s.ensureIssueForTask(ctx, row.Task, &comp, repoURL, repoSlug)
			if err != nil {
				row.IssueOK = false
				row.IssueErr = err.Error()
				row.Task.Status = string(models.TaskStatusFailed)
				row.Task.LifecycleStatus = string(models.TaskLifecycleGhIssueFailed)
				row.Task.ErrorMessage = "github.create_failed: " + err.Error()
				cause := "github.create_failed"
				row.Task.Cause = &cause
				if uerr := s.taskRepo.Update(ctx, row.Task); uerr != nil {
					slog.WarnContext(ctx, "persist task failure state", "task", row.Task.ID, "error", uerr)
				}
				w.send("data-task-issue-failed", map[string]any{
					"tempId":    row.TempID,
					"errorText": err.Error(),
				})
				return
			}
			row.IssueOK = true
			w.send("data-task-issued", map[string]any{
				"tempId":      row.TempID,
				"taskId":      row.Task.ID,
				"issueUrl":    row.Task.IssueURL,
				"issueNumber": row.Task.IssueNumber,
			})
		}()
	}
	wg.Wait()
	return rows, nil
}

func ptrString(s string) *string { return &s }

// =============================================================================
// Phase 2 — detail stream proxy
// =============================================================================

func (s *taskService) proxyDetailStream(
	ctx context.Context,
	upstream io.Reader,
	w *sseWriter,
	persisted []persistedItem,
	repoURL, repoSlug string,
) {
	taskByID := make(map[string]*models.ComponentTask, len(persisted))
	for _, p := range persisted {
		taskByID[p.Task.ID] = p.Task
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			w.passthrough(line)
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var head struct {
			Type string `json:"type"`
			Data json.RawMessage
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			w.passthrough(line)
			continue
		}
		switch head.Type {
		case "data-task-body-delta":
			w.passthrough(line)
		case "data-task-body-complete":
			w.passthrough(line)
			var d struct {
				TaskID string `json:"taskId"`
				Body   string `json:"body"`
			}
			if err := json.Unmarshal(head.Data, &d); err != nil {
				continue
			}
			task, ok := taskByID[d.TaskID]
			if !ok {
				continue
			}
			task.Body = d.Body
			if err := s.taskRepo.Update(ctx, task); err != nil {
				slog.WarnContext(ctx, "persist task body", "task", task.ID, "error", err)
				continue
			}
			// Async issue body edit. The DB row is canonical for dispatch;
			// if GH edit fails persistently the row carries body_sync_pending.
			go s.editIssueBodyWithRetries(context.Background(), task, repoURL, repoSlug)
		case "error":
			w.passthrough(line)
		default:
			w.passthrough(line)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.WarnContext(ctx, "detail stream scan", "error", err)
	}
}

func (s *taskService) editIssueBodyWithRetries(ctx context.Context, task *models.ComponentTask, repoURL, repoSlug string) {
	if s.gitClient == nil || task.IssueNumber == 0 {
		return
	}
	comp, _ := s.resolveDesignComponent(ctx, task)
	body := buildIssueBody(task, comp, repoURL, repoSlug)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		err := s.gitClient.EditIssueBody(ctx, task.OrgID, task.ProjectID, task.IssueNumber, body)
		if err == nil {
			task.BodySyncPending = false
			_ = s.taskRepo.Update(ctx, task)
			return
		}
		lastErr = err
	}
	task.BodySyncPending = true
	_ = s.taskRepo.Update(ctx, task)
	slog.WarnContext(ctx, "edit issue body failed after retries",
		"task", task.ID, "issue", task.IssueNumber, "error", lastErr)
}

// =============================================================================
// Reconciliation
// =============================================================================

// runReconciliationStreamed closes pending tasks for components removed from
// the current design. Emits data-task-rejected to the console for each.
// Counterpart to ReconcilePendingForDesignChange (no-SSE variant).
func (s *taskService) runReconciliationStreamed(ctx context.Context, orgID, projectID string, design *DesignFile, w *sseWriter) error {
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return err
	}
	current := make(map[string]struct{}, len(design.Components))
	for _, c := range design.Components {
		current[strings.ToLower(c.Name)] = struct{}{}
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != string(models.TaskStatusPending) {
			continue
		}
		if _, ok := current[strings.ToLower(t.ComponentName)]; ok {
			continue
		}
		// Component removed — close issue and reject task.
		if s.gitClient != nil && t.IssueNumber > 0 {
			if err := s.gitClient.CloseIssue(ctx, orgID, projectID, t.IssueNumber, "Component removed from architecture; auto-closed by tech-lead reconciliation."); err != nil {
				slog.WarnContext(ctx, "close issue on reconciliation", "task", t.ID, "issue", t.IssueNumber, "error", err)
			}
		}
		t.Status = string(models.TaskStatusRejected)
		cause := "design.removed"
		t.Cause = &cause
		if err := s.taskRepo.Update(ctx, t); err != nil {
			slog.WarnContext(ctx, "persist reconciliation status", "task", t.ID, "error", err)
			continue
		}
		w.send("data-task-rejected", map[string]any{
			"taskId": t.ID,
			"reason": "design.removed",
		})
	}
	return nil
}

// ReconcilePendingForDesignChange is the non-streaming counterpart called
// from design_service.SaveAndProceed after the tag bump. Closes pending
// tasks for removed components; idempotent and emits no SSE.
func (s *taskService) ReconcilePendingForDesignChange(ctx context.Context, orgID, projectID string) error {
	design, err := s.store.ReadDesign(ctx, orgID, projectID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read design: %w", err)
	}
	if design == nil {
		return nil
	}
	tasks, err := s.taskRepo.ListByProjectID(ctx, orgID, projectID)
	if err != nil {
		return err
	}
	current := make(map[string]struct{}, len(design.Components))
	for _, c := range design.Components {
		current[strings.ToLower(c.Name)] = struct{}{}
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != string(models.TaskStatusPending) {
			continue
		}
		if _, ok := current[strings.ToLower(t.ComponentName)]; ok {
			continue
		}
		if s.gitClient != nil && t.IssueNumber > 0 {
			if err := s.gitClient.CloseIssue(ctx, orgID, projectID, t.IssueNumber, "Component removed from architecture; auto-closed by tech-lead reconciliation."); err != nil {
				slog.WarnContext(ctx, "close issue on reconciliation", "task", t.ID, "issue", t.IssueNumber, "error", err)
			}
		}
		t.Status = string(models.TaskStatusRejected)
		cause := "design.removed"
		t.Cause = &cause
		if err := s.taskRepo.Update(ctx, t); err != nil {
			slog.WarnContext(ctx, "persist reconciliation status", "task", t.ID, "error", err)
		}
	}
	return nil
}

// =============================================================================
// RegenerateTaskBody — single-task Phase 2 retry
// =============================================================================

func (s *taskService) RegenerateTaskBody(ctx context.Context, taskID string, out io.Writer, flush func()) error {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	if task == nil {
		return ErrTaskNotFound
	}
	reqFiles, err := s.store.ListRequirements(ctx, task.OrgID, task.ProjectID)
	if err != nil {
		return fmt.Errorf("list requirements: %w", err)
	}
	specContent := concatRequirementBundle(reqFiles)
	design, err := s.store.ReadDesign(ctx, task.OrgID, task.ProjectID)
	if err != nil || design == nil {
		return fmt.Errorf("read design: %w", err)
	}
	allTasks, _ := s.taskRepo.ListByProjectID(ctx, task.OrgID, task.ProjectID)

	survived := []persistedItem{{
		TempID: "p-retry",
		Task:   task,
	}}
	// RetryTask path: re-resolve the project's attached skills from the
	// live design. Snapshot-aware path (read from
	// design_version_skill_snapshots for task.SourceDesignVersion) lands
	// in PR 3 when the snapshot writer is wired.
	_, resolvedSkills := s.resolveProjectSkills(ctx, task.OrgID, design)
	detailReq := buildDetailRequest(task.ProjectID, specContent, survived, design, allTasks, resolvedSkills)
	upstream, err := s.agentsClient.StreamTechLeadDetail(ctx, task.OrgID, detailReq)
	if err != nil {
		return fmt.Errorf("detail upstream: %w", err)
	}
	defer upstream.Close()

	w := newSseWriter(out, flush)
	repoURL, repoSlug := s.repoInfoForBody(ctx, task.OrgID, task.ProjectID)
	s.proxyDetailStream(ctx, upstream, w, survived, repoURL, repoSlug)
	w.send("data-finish", map[string]any{"taskCount": 1})
	return nil
}

// =============================================================================
// Helpers
// =============================================================================

func (s *taskService) repoInfoForBody(ctx context.Context, orgID, projectID string) (string, string) {
	if s.gitClient == nil {
		return "", ""
	}
	repo, err := s.gitClient.GetRepo(ctx, orgID, projectID)
	if err != nil || repo == nil {
		return "", ""
	}
	return repo.RepoURL, repo.RepoSlug
}

func (s *taskService) currentArtifactVersions(ctx context.Context, orgID, projectID string) (specV, designV string) {
	if s.gitClient == nil {
		return "", ""
	}
	if vs, err := s.gitClient.ListRequirementsVersions(ctx, orgID, projectID); err == nil && len(vs) > 0 {
		specV = vs[0].Tag
	}
	if vs, err := s.gitClient.ListDesignVersions(ctx, orgID, projectID); err == nil && len(vs) > 0 {
		designV = vs[0].Tag
	}
	return specV, designV
}

// filterNonRejectedForPrompt returns the existing-task list to ship to the
// planner. Excludes rejected/failed/abandoned tasks (rejected work falls out
// of the "do not duplicate" set; the model can re-propose it). See design §7.
func filterNonRejectedForPrompt(tasks []models.ComponentTask) []agents.TechLeadExistingTaskSummary {
	var out []agents.TechLeadExistingTaskSummary
	for _, t := range tasks {
		switch t.Status {
		case string(models.TaskStatusRejected), string(models.TaskStatusFailed), string(models.TaskStatusAbandoned):
			continue
		}
		var num *int
		if t.IssueNumber > 0 {
			n := t.IssueNumber
			num = &n
		}
		out = append(out, agents.TechLeadExistingTaskSummary{
			IssueNumber:   num,
			Title:         t.Title,
			ComponentName: t.ComponentName,
			Status:        t.Status,
		})
	}
	return out
}

func buildPlanRequest(
	projectName, spec string,
	components []models.DesignComponent,
	designDiff DesignDiff,
	specDiff string,
	existingForPrompt []agents.TechLeadExistingTaskSummary,
	mode string,
	attachedSkills []agents.SkillDescription,
) agents.TechLeadPlanRequest {
	slim := make([]agents.TechLeadSlimComponent, len(components))
	for i, c := range components {
		dep := c.DependsOn
		if dep == nil {
			dep = []string{}
		}
		slim[i] = agents.TechLeadSlimComponent{
			Name:          c.Name,
			ComponentType: c.ComponentType,
			Language:      c.Language,
			DependsOn:     dep,
		}
	}

	req := agents.TechLeadPlanRequest{
		ProjectName:    projectName,
		Spec:           spec,
		SlimDesign:     slim,
		Mode:           mode,
		ExistingTasks:  existingForPrompt,
		AttachedSkills: attachedSkills,
	}
	if mode == "incremental" {
		req.SpecDiff = specDiff
		req.DesignDiff = renderDesignDiffForPrompt(designDiff)
	}

	// Ship the validator-side diff context regardless of mode so the
	// validator's coverage rules can fire.
	addedNames := make([]string, 0, len(designDiff.Added))
	for _, a := range designDiff.Added {
		addedNames = append(addedNames, a.Name)
	}
	contractAffected := make([]string, 0, len(designDiff.Modified))
	for _, m := range designDiff.Modified {
		if m.ContractAffected {
			contractAffected = append(contractAffected, m.Name)
		}
	}
	removedNames := make([]string, 0, len(designDiff.Removed))
	for _, r := range designDiff.Removed {
		removedNames = append(removedNames, r.Name)
	}
	req.Diff = &agents.TechLeadValidatorDiffContext{
		Added:                    addedNames,
		ContractAffectedModified: contractAffected,
		Removed:                  removedNames,
	}
	return req
}

func buildDetailRequest(
	projectName, spec string,
	persisted []persistedItem,
	design *DesignFile,
	allTasks []models.ComponentTask,
	resolvedSkills []agents.SkillRecord,
) agents.TechLeadDetailRequest {
	byName := make(map[string]models.DesignComponent, len(design.Components))
	for _, c := range design.Components {
		byName[strings.ToLower(c.Name)] = c
	}

	// existingTitlesForComponent — group prior tasks by component, exclude
	// rejected (per design §7) and exclude the just-persisted items in this
	// batch (they're not "existing" relative to the detail phase).
	persistedIDs := make(map[string]struct{}, len(persisted))
	for _, p := range persisted {
		persistedIDs[p.Task.ID] = struct{}{}
	}
	titlesByComp := map[string][]agents.TechLeadExistingTitle{}
	for _, t := range allTasks {
		if t.Status == string(models.TaskStatusRejected) {
			continue
		}
		if _, ok := persistedIDs[t.ID]; ok {
			continue
		}
		key := strings.ToLower(t.ComponentName)
		titlesByComp[key] = append(titlesByComp[key], agents.TechLeadExistingTitle{
			Title:  t.Title,
			Status: t.Status,
		})
	}

	items := make([]agents.TechLeadDetailItem, 0, len(persisted))
	for _, p := range persisted {
		comp := byName[strings.ToLower(p.Task.ComponentName)]
		// Slim summaries for dependsOn components. Initialised non-nil so it
		// marshals as `[]` rather than `null` — agents-service's Zod schema
		// rejects null arrays.
		depSummaries := make([]agents.TechLeadSlimComponent, 0, len(comp.DependsOn))
		for _, d := range comp.DependsOn {
			dep, ok := byName[strings.ToLower(d)]
			if !ok {
				continue
			}
			depDeps := dep.DependsOn
			if depDeps == nil {
				depDeps = []string{}
			}
			depSummaries = append(depSummaries, agents.TechLeadSlimComponent{
				Name:          dep.Name,
				ComponentType: dep.ComponentType,
				Language:      dep.Language,
				DependsOn:     depDeps,
			})
		}

		designSlice := p.DesignSli
		if designSlice == "" {
			compForPrompt := comp
			compForPrompt.OpenAPISpec = ""
			b, _ := json.Marshal(compForPrompt)
			designSlice = string(b)
		}

		// Same null→[] coercion for existing titles.
		existingTitles := titlesByComp[strings.ToLower(p.Task.ComponentName)]
		if existingTitles == nil {
			existingTitles = []agents.TechLeadExistingTitle{}
		}

		items = append(items, agents.TechLeadDetailItem{
			TaskID:                     p.Task.ID,
			ComponentName:              p.Task.ComponentName,
			Title:                      p.Task.Title,
			Rationale:                  p.Task.Rationale,
			DesignSlice:                designSlice,
			DepSummaries:               depSummaries,
			ExistingTitlesForComponent: existingTitles,
			SkillsResolved:             resolvedSkills,
		})
	}
	return agents.TechLeadDetailRequest{
		ProjectName: projectName,
		Spec:        spec,
		Items:       items,
	}
}

// resolveProjectSkills resolves the design's `skillsApplied` list into
// (descriptions for the planner, full bodies for the detail phase).
// Empty slices when SkillService isn't wired or skillsApplied is empty.
// See docs/design/skills-system.md > "Tech-lead".
func (s *taskService) resolveProjectSkills(ctx context.Context, orgID string, design *DesignFile) ([]agents.SkillDescription, []agents.SkillRecord) {
	if s.skillSvc == nil || design == nil || len(design.SkillsApplied) == 0 {
		return nil, nil
	}
	resolved, err := s.skillSvc.ResolveMany(ctx, orgID, design.SkillsApplied)
	if err != nil {
		slog.WarnContext(ctx, "tech-lead: skill resolve failed — continuing without", "orgID", orgID, "error", err)
		return nil, nil
	}
	descs := make([]agents.SkillDescription, 0, len(resolved))
	records := make([]agents.SkillRecord, 0, len(resolved))
	for _, sk := range resolved {
		descs = append(descs, agents.SkillDescription{
			Name:        sk.Name,
			Description: sk.Description,
		})
		records = append(records, agents.SkillRecord{
			Name:        sk.Name,
			Description: sk.Description,
			Body:        sk.SkillMD,
		})
	}
	return descs, records
}

// AssembleDesign is defined in artifact_store.go.

// shadow to keep the lint quiet for the unused result on advisory lock scan.
var _ = strconv.Itoa
var _ = errors.Is
