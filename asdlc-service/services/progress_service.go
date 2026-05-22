package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/wso2/asdlc/asdlc-service/clients/observer"
	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// Default cap on lines per /progress/* response. Keeps Observer query
// cost bounded — design §6.5.
const defaultProgressLimit = 200

// ProgressResponse is the shape returned by both /progress/agent and
// /progress/build. Schema-versioned so the console can branch on future
// envelope changes without flag-flipping.
type ProgressResponse struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Lines         []observer.ProgressEvent  `json:"lines"`
	CursorMillis  int64                     `json:"cursorMillis"`
	Phase         string                    `json:"phase,omitempty"`
	Truncated     bool                      `json:"truncated,omitempty"`
	Final         bool                      `json:"final"`
}

// ProgressService backs the BFF's /progress/* endpoints.
type ProgressService interface {
	// GetAgentProgress returns coding-agent NDJSON lines emitted at or
	// after sinceMillis (inclusive). When sinceMillis is 0 the service
	// substitutes (task.DispatchedAt - 1s); when the task has no run
	// recorded yet the response is empty + Final=false.
	GetAgentProgress(ctx context.Context, taskID string, sinceMillis int64, limit int) (*ProgressResponse, error)

	// GetBuildProgress returns synthetic build_step lines derived from
	// the build WorkflowRun's Status.Tasks[]. Same cursor semantics as
	// GetAgentProgress. Final=true once the task is past `building`.
	GetBuildProgress(ctx context.Context, taskID string, sinceMillis int64) (*ProgressResponse, error)
}

type progressService struct {
	taskSvc        TaskService
	ocClient       openchoreo.ComponentClient
	observerClient observer.Client

	// Singleflight collapses N concurrent viewers of the same
	// (runName, sinceMillis-bucket) into one Observer call. The leader
	// owns the upstream context with a 10s cap; followers wait on the
	// channel.
	sf singleflight.Group

	// In-memory cache of last-seen per-step phase used to compute
	// build_step deltas. Outer key = taskID (so eviction on terminal
	// task status drops the whole tree); inner key = stepName.
	mu       sync.Mutex
	stepSeen map[string]map[string]string

	// Monotonic seq for synthetic build_step events. Only used to break
	// ties on identical timestamps.
	buildSeq int64
}

// NewProgressService constructs a progress service. observerClient may
// be nil — methods then return ErrProgressUnavailable so the controller
// can map to 503.
//
// The workflow plane namespace is derived per-call from the task's
// OrgID (== OC namespace name): Observer queries take the source OC
// namespace and prepend `workflows-` to address the actual K8s
// namespace where Argo schedules pods. There is no platform-wide
// workflow-plane namespace once orgs are not collapsed.
func NewProgressService(
	taskSvc TaskService,
	ocClient openchoreo.ComponentClient,
	observerClient observer.Client,
) ProgressService {
	return &progressService{
		taskSvc:        taskSvc,
		ocClient:       ocClient,
		observerClient: observerClient,
		stepSeen:       map[string]map[string]string{},
	}
}

// ErrProgressUnavailable signals that the Observer is not reachable.
// The controller maps this to HTTP 503 progress_unavailable.
var ErrProgressUnavailable = errors.New("progress unavailable")

func (s *progressService) GetAgentProgress(ctx context.Context, taskID string, sinceMillis int64, limit int) (*ProgressResponse, error) {
	if s.observerClient == nil {
		return nil, ErrProgressUnavailable
	}
	if limit <= 0 || limit > defaultProgressLimit {
		limit = defaultProgressLimit
	}

	task, err := s.taskSvc.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	final := !isAgentActive(task)
	resp := &ProgressResponse{
		SchemaVersion: observer.ProgressSchemaVersion,
		Lines:         []observer.ProgressEvent{},
		CursorMillis:  sinceMillis,
		Final:         final,
	}
	if task.LastCodingAgentRunName == "" {
		// Run not provisioned yet — nothing to report. Cursor is unchanged.
		return resp, nil
	}

	sinceTime := resolveSinceTime(sinceMillis, task.DispatchedAt)
	lines, err := s.fetchObserverLogs(ctx, task.LastCodingAgentRunName, task.OrgID, sinceTime, limit+1)
	if err != nil {
		return nil, err
	}

	events := parseAndSort(lines)
	if len(events) > limit {
		events = events[:limit]
		resp.Truncated = true
	}
	resp.Lines = events
	resp.CursorMillis = nextCursor(events, sinceMillis)
	resp.Phase = lastPhase(events)
	return resp, nil
}

func (s *progressService) GetBuildProgress(ctx context.Context, taskID string, sinceMillis int64) (*ProgressResponse, error) {
	task, err := s.taskSvc.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	final := !isBuildActive(task)
	resp := &ProgressResponse{
		SchemaVersion: observer.ProgressSchemaVersion,
		Lines:         []observer.ProgressEvent{},
		CursorMillis:  sinceMillis,
		Final:         final,
	}
	if task.LastBuildRunName == "" {
		return resp, nil
	}

	run, err := s.ocClient.GetWorkflowRun(ctx, task.OrgID, task.LastBuildRunName)
	if err != nil {
		return nil, fmt.Errorf("get build run: %w", err)
	}

	events := s.diffBuildSteps(task.ID, run, sinceMillis)
	resp.Lines = events
	resp.CursorMillis = nextCursor(events, sinceMillis)
	resp.Phase = lastPhase(events)
	if run.Completed {
		resp.Final = true
		// Once the run is terminal there will be no more diffs; clear
		// the per-task entry so the map doesn't accumulate forever.
		s.evictStepSeen(task.ID)
	}
	return resp, nil
}

// diffBuildSteps emits a build_step event for each task whose phase
// changed since the last observation, plus any task whose StartedAt is
// strictly after sinceMillis. Stateful via stepSeen so the same step is
// not emitted twice for the same phase. Per-task entries are evicted
// when the run reaches terminal state (see GetBuildProgress).
func (s *progressService) diffBuildSteps(taskID string, run *models.WorkflowRun, sinceMillis int64) []observer.ProgressEvent {
	if run == nil {
		return nil
	}
	out := make([]observer.ProgressEvent, 0, len(run.Tasks))
	s.mu.Lock()
	defer s.mu.Unlock()
	taskSteps, ok := s.stepSeen[taskID]
	if !ok {
		taskSteps = map[string]string{}
		s.stepSeen[taskID] = taskSteps
	}
	for _, step := range run.Tasks {
		if taskSteps[step.Name] == step.Phase {
			continue
		}
		taskSteps[step.Name] = step.Phase
		ts := pickStepTs(step)
		if ts != "" && sinceMillis > 0 {
			if t, err := time.Parse(time.RFC3339, ts); err == nil && t.UnixMilli() < sinceMillis {
				continue
			}
		}
		s.buildSeq++
		out = append(out, observer.ProgressEvent{
			SchemaVersion: observer.ProgressSchemaVersion,
			Ts:            ts,
			Seq:           s.buildSeq,
			Kind:          "build_step",
			Step:          step.Name,
			Phase:         step.Phase,
			Message:       step.Message,
			StartedAt:     step.StartedAt,
			CompletedAt:   step.CompletedAt,
		})
	}
	sortEvents(out)
	return out
}

func (s *progressService) evictStepSeen(taskID string) {
	s.mu.Lock()
	delete(s.stepSeen, taskID)
	s.mu.Unlock()
}

// fetchObserverLogs wraps the Observer call in a singleflight bucket so
// concurrent viewers of the same (runName, sinceMillis-bucket) share
// one upstream call. The leader owns a detached context with a 10s
// timeout — followers wait on the channel and don't fall through to a
// non-deduped direct call (matches agent-manager's singleflight idiom
// in clients/thundersvc/client.go).
//
// ouHandle is the OC org namespace name (== ouHandle); Observer prepends
// `workflows-` to address the workflow-plane namespace where Argo
// schedules the pod that produced the logs.
func (s *progressService) fetchObserverLogs(ctx context.Context, runName, ouHandle string, sinceTime time.Time, limit int) ([]observer.LogLine, error) {
	bucket := sinceTime.Unix()
	key := fmt.Sprintf("agent|%s|%s|%d", runName, ouHandle, bucket)
	type result struct {
		lines []observer.LogLine
		err   error
	}
	ch := s.sf.DoChan(key, func() (any, error) {
		// Detach from the caller's context so a follower cancelling
		// doesn't kill the leader's upstream call. 10s hard cap keeps
		// a stuck Observer bounded.
		callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		lines, err := s.observerClient.GetWorkflowRunLogs(callCtx, runName, ouHandle, sinceTime, limit)
		return result{lines: lines, err: err}, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		res := r.Val.(result)
		if errors.Is(res.err, observer.ErrUnavailable) {
			return nil, ErrProgressUnavailable
		}
		if res.err != nil {
			return nil, res.err
		}
		return res.lines, nil
	}
}

func parseAndSort(lines []observer.LogLine) []observer.ProgressEvent {
	if len(lines) == 0 {
		return []observer.ProgressEvent{}
	}
	out := make([]observer.ProgressEvent, 0, len(lines))
	for _, l := range lines {
		ev := observer.ParseProgressLine(l.Log)
		if ev.Ts == "" && !l.Timestamp.IsZero() {
			ev.Ts = l.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, ev)
	}
	sortEvents(out)
	out = dedupOnTsSeq(out)
	return out
}

func sortEvents(events []observer.ProgressEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Ts != events[j].Ts {
			return events[i].Ts < events[j].Ts
		}
		return events[i].Seq < events[j].Seq
	})
}

// dedupOnTsSeq removes consecutive entries with identical (ts, seq).
// The Observer can return overlapping windows on cursor edges; the
// design (§6.5) specifies dedup on this composite key.
func dedupOnTsSeq(events []observer.ProgressEvent) []observer.ProgressEvent {
	if len(events) < 2 {
		return events
	}
	out := events[:1]
	for i := 1; i < len(events); i++ {
		prev := out[len(out)-1]
		cur := events[i]
		if cur.Ts == prev.Ts && cur.Seq == prev.Seq && cur.Seq != 0 {
			continue
		}
		out = append(out, cur)
	}
	return out
}

func lastPhase(events []observer.ProgressEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "phase" && events[i].Phase != "" {
			return events[i].Phase
		}
	}
	return ""
}

func nextCursor(events []observer.ProgressEvent, fallback int64) int64 {
	cursor := fallback
	for _, ev := range events {
		if ev.Ts == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, ev.Ts)
		if err != nil {
			t, err = time.Parse(time.RFC3339, ev.Ts)
			if err != nil {
				continue
			}
		}
		ms := t.UnixMilli()
		if ms+1 > cursor {
			cursor = ms + 1
		}
	}
	return cursor
}

func resolveSinceTime(sinceMillis int64, dispatchedAt *time.Time) time.Time {
	if sinceMillis > 0 {
		return time.UnixMilli(sinceMillis)
	}
	if dispatchedAt != nil && !dispatchedAt.IsZero() {
		return dispatchedAt.Add(-1 * time.Second)
	}
	return time.Time{}
}

func pickStepTs(step models.WorkflowRunTask) string {
	if step.CompletedAt != "" {
		return step.CompletedAt
	}
	if step.StartedAt != "" {
		return step.StartedAt
	}
	return ""
}

func isAgentActive(task *models.ComponentTask) bool {
	return task.Status == string(models.TaskStatusInProgress) ||
		task.Status == string(models.TaskStatusTesting)
}

func isBuildActive(task *models.ComponentTask) bool {
	return task.Status == string(models.TaskStatusBuilding)
}
