package services

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/agents"
	"github.com/wso2/asdlc/asdlc-service/clients/gitservice"
)

// RequirementsChatService streams a chat turn between the user and the
// requirements-chat agent. The agent runs in agents-service; this service
// is the persistence layer — it re-applies every successful tool result
// against the live working tree and re-renders DSL siblings, then forwards
// the resulting frames to the browser.
//
// See docs/design/requirements-chat.md §4.4 for the topology.
type RequirementsChatService interface {
	StreamChat(ctx context.Context, orgID, projectID string, req ChatTurnRequest, w io.Writer, flush func()) error
	UndoTurn(ctx context.Context, orgID, projectID, turnID string) (map[string]string, error)
	GetSessionBaselineFile(ctx context.Context, orgID, projectID, baselineID, filename string) (*gitservice.RequirementsSnapshotFile, error)
	DropSessionBaseline(ctx context.Context, orgID, projectID, baselineID string) error
	RevertFileToBaseline(ctx context.Context, orgID, projectID, baselineID, filename string) error
}

// ChatTurnRequest is the wire shape posted by the console.
type ChatTurnRequest struct {
	Message string                      `json:"message"`
	History []agents.ChatHistoryMessage `json:"history"`
	// Optional subset of in-scope filenames. Empty == "all files in the
	// working tree".
	ScopeFiles []string `json:"files,omitempty"`
	Mode       string   `json:"mode"` // "edit" | "ask"
	// When true, the BFF captures a session baseline snapshot (separate
	// from the per-turn snapshot) BEFORE applying any tool results, then
	// emits a `data-session-baseline` SSE frame so the console can persist
	// the ID. Per-file Accept / Revert reads from this snapshot. The
	// console sets this on the first turn after it has no baseline; once
	// captured, subsequent turns leave the flag false.
	RequestSessionBaseline bool `json:"requestSessionBaseline,omitempty"`
}

type requirementsChatService struct {
	store        *ArtifactStore
	agentsClient agents.Client
	gitClient    gitservice.Client
	locker       *RequirementsDirLocker
}

func NewRequirementsChatService(
	store *ArtifactStore,
	agentsClient agents.Client,
	gitClient gitservice.Client,
	locker *RequirementsDirLocker,
) RequirementsChatService {
	return &requirementsChatService{
		store:        store,
		agentsClient: agentsClient,
		gitClient:    gitClient,
		locker:       locker,
	}
}

// SSEWriter is a small helper to write a single JSON frame as an SSE
// data: line and flush. Kept inline because every existing streaming
// route in this service does the same dance.
type chatSSEWriter struct {
	w     io.Writer
	flush func()
}

func (s *chatSSEWriter) frame(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(payload); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	s.flush()
	return nil
}

// StreamChat is the main entrypoint. Flow:
//
//  1. Acquire the session-scoped dir lock. 409 if busy.
//  2. Read the working-tree file map; package the in-scope subset for
//     the agent.
//  3. Capture a snapshot ref for per-turn undo.
//  4. Open upstream SSE to agents-service.
//  5. Forward frames; intercept `data-tool-result` to apply the tool's
//     post-edit content to the live working tree (via git-service).
//  6. Close cleanly on `data-finish`, `error`, or context cancellation.
//
// On any fatal error, emit a `data-error` frame downstream so the UI
// stops the soft lock.
func (s *requirementsChatService) StreamChat(ctx context.Context, orgID, projectID string, req ChatTurnRequest, w io.Writer, flush func()) error {
	sse := &chatSSEWriter{w: w, flush: flush}

	// 1. Acquire lock. Non-blocking — we want a fast 409 if another
	//    writer holds it (could be a manual PUT, a generate stream, or
	//    another chat tab).
	lock, err := s.locker.AcquireSession(ctx, orgID, projectID)
	if err != nil {
		if errors.Is(err, RequirementsDirLockBusy) {
			_ = sse.frame(map[string]any{
				"type":      "error",
				"errorCode": "chat_in_progress",
				"errorText": "Another writer is editing the requirements (manual save / generate / chat). Try again in a moment.",
			})
			return nil
		}
		return fmt.Errorf("acquire dir lock: %w", err)
	}
	defer lock.Release(ctx)

	// 2. Load the working tree.
	allFiles, err := s.store.ListRequirements(ctx, orgID, projectID)
	if err != nil {
		return fmt.Errorf("list requirements: %w", err)
	}
	scope := filterScope(allFiles, req.ScopeFiles)

	// 3a. Capture per-turn undo snapshot.
	turnID := "t_" + randomID()
	if _, err := s.gitClient.CaptureRequirementsSnapshot(ctx, orgID, projectID, turnID); err != nil {
		slog.WarnContext(ctx, "capture snapshot failed (continuing without undo)",
			"project", projectID, "turn", turnID, "error", err)
		// Non-fatal — the turn can still proceed; the user just won't have
		// an undo button.
	}

	// 3b. Optionally capture session baseline (separate from per-turn).
	// Emits `data-session-baseline` so the console can persist the ID and
	// drive per-file Accept / Revert against this fixed point.
	if req.RequestSessionBaseline {
		baselineID := "sb_" + randomID()
		if _, err := s.gitClient.CaptureRequirementsSnapshot(ctx, orgID, projectID, baselineID); err != nil {
			slog.WarnContext(ctx, "capture session baseline failed (continuing without per-file undo)",
				"project", projectID, "baseline", baselineID, "error", err)
		} else {
			if err := sse.frame(map[string]any{
				"type": "data-session-baseline",
				"data": map[string]any{
					"snapshotId": baselineID,
				},
			}); err != nil {
				return err
			}
			slog.InfoContext(ctx, "requirements chat session baseline captured",
				"project", projectID, "baseline", baselineID)
		}
	}

	// Emit the turn-started frame so the UI can stash the turnId on the
	// just-sent user message. Wire format mirrors what agents-service
	// emits: `{type: "data-<event>", data: {...}}`.
	startedAt := time.Now().UnixMilli()
	if err := sse.frame(map[string]any{
		"type": "data-turn-started",
		"data": map[string]any{
			"turnId":  turnID,
			"started": startedAt,
		},
	}); err != nil {
		return err
	}

	slog.InfoContext(ctx, "requirements chat turn started",
		"project", projectID, "turn", turnID,
		"scopeFiles", len(scope), "mode", req.Mode)

	// 4. Open upstream SSE.
	upstream, err := s.agentsClient.StreamRequirementsChat(ctx, orgID, agents.RequirementsChatRequest{
		Message: req.Message,
		History: req.History,
		Files:   scope,
		Mode:    fallbackMode(req.Mode),
	})
	if err != nil {
		_ = sse.frame(map[string]any{
			"type":      "error",
			"errorText": fmt.Sprintf("agents-service unavailable: %v", err),
		})
		return nil
	}
	defer upstream.Close()

	// 5. Stream loop.
	scanner := bufio.NewScanner(upstream)
	// Per-frame buffer: tool-result frames can carry a full file's
	// content. 4 MiB is well above the 256 KiB file-size cap.
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			// Pass-through (keep-alive comments, blank lines).
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}

		var frame map[string]any
		if err := json.Unmarshal(payload, &frame); err != nil {
			// Forward unparseable frames untouched — defensive.
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flush()
			continue
		}

		ty, _ := frame["type"].(string)
		switch ty {
		case "data-tool-result":
			// Apply the tool's post-edit content against the live working
			// tree. The agent ran against a snapshot taken at request
			// start; thanks to the dir lock, the snapshot has not drifted.
			if err := s.applyToolResult(ctx, orgID, projectID, frame); err != nil {
				// Extract the upstream id so the UI can re-key the running
				// tool card. Agents-service emits its events with
				// `{type, data: {id, ...}}` — keep that envelope.
				var id any
				if d, ok := frame["data"].(map[string]any); ok {
					id = d["id"]
				}
				_ = sse.frame(map[string]any{
					"type": "data-tool-error",
					"data": map[string]any{
						"id":        id,
						"errorCode": "apply_failed",
						"message":   err.Error(),
					},
				})
				slog.ErrorContext(ctx, "tool result apply failed",
					"project", projectID, "turn", turnID, "error", err)
				continue
			}
			// Frame was potentially augmented in-place — re-serialise.
			out, _ := json.Marshal(frame)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(out)
			_, _ = w.Write([]byte("\n\n"))
			flush()

		case "data-finish":
			// Forward and exit the loop.
			out, _ := json.Marshal(frame)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(out)
			_, _ = w.Write([]byte("\n\n"))
			flush()
			slog.InfoContext(ctx, "requirements chat turn finished",
				"project", projectID, "turn", turnID,
				"durationMs", time.Now().UnixMilli()-startedAt)

		case "error":
			// Fatal upstream error — forward, then stop reading.
			out, _ := json.Marshal(frame)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(out)
			_, _ = w.Write([]byte("\n\n"))
			flush()

		default:
			// All other frame types (text-delta, data-tool-started,
			// data-tool-error, data-validation-failed) are pass-through.
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		// Stream-read failures (e.g. agents-service crashed mid-stream).
		// Emit a friendly error frame so the UI clears the soft lock.
		_ = sse.frame(map[string]any{
			"type":      "error",
			"errorText": fmt.Sprintf("upstream stream interrupted: %v", err),
		})
	}
	return nil
}

// applyToolResult re-applies the agent's edit against the LIVE working
// tree (defence in depth — agents-service might lose a frame; the dir
// lock prevents other writers from drifting it). For canvas DSL writes,
// also re-renders the .excalidraw sibling via agents-service.
//
// Mutates the frame in-place to augment the wire payload with the
// authoritative `content` field and any siblings the BFF rendered.
func (s *requirementsChatService) applyToolResult(ctx context.Context, orgID, projectID string, frame map[string]any) error {
	data, ok := frame["data"].(map[string]any)
	if !ok {
		return errors.New("tool-result frame missing data")
	}
	filename, _ := data["filename"].(string)
	content, _ := data["content"].(string)
	if filename == "" {
		return errors.New("tool-result frame missing filename")
	}

	// Persist primary write. Empty content + a name not in the working
	// tree is interpreted as a delete (the agent's delete_file path).
	current, readErr := s.store.ReadRequirementFile(ctx, orgID, projectID, filename)
	isDelete := strings.HasSuffix(strings.ToLower(filename), ".md") &&
		content == "" &&
		readErr == nil

	if isDelete {
		if err := s.store.DeleteRequirementFile(ctx, orgID, projectID, filename); err != nil {
			return fmt.Errorf("delete %s: %w", filename, err)
		}
	} else {
		if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, filename, content); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
	}
	_ = current // reserved for future "BFF re-runs str_replace against current" defence

	// Canvas DSL: re-render the sibling .excalidraw via agents-service.
	if strings.HasSuffix(strings.ToLower(filename), ".dsl") {
		kind := ""
		switch {
		case strings.HasPrefix(strings.ToLower(filename), "wireframes"):
			kind = "wireframes"
		case strings.HasPrefix(strings.ToLower(filename), "domain-model"):
			kind = "domain-model"
		}
		if kind != "" {
			rendered, err := s.agentsClient.RenderDsl(ctx, kind, content)
			if err != nil {
				return fmt.Errorf("render %s: %w", filename, err)
			}
			sibling := strings.TrimSuffix(filename, ".dsl") + ".excalidraw"
			if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, sibling, rendered); err != nil {
				return fmt.Errorf("write %s: %w", sibling, err)
			}
			// Augment the frame so the browser refreshes the canvas buffer.
			siblings, _ := data["siblings"].(map[string]any)
			if siblings == nil {
				siblings = map[string]any{}
			}
			siblings[sibling] = rendered
			data["siblings"] = siblings
		}
	}

	slog.DebugContext(ctx, "tool-result applied",
		"project", projectID, "filename", filename, "bytes", len(content))
	return nil
}

// UndoTurn restores the requirements directory to the snapshot captured at
// the start of `turnID`. Acquires the dir lock for the duration.
func (s *requirementsChatService) UndoTurn(ctx context.Context, orgID, projectID, turnID string) (map[string]string, error) {
	if turnID == "" {
		return nil, fmt.Errorf("turnId is required")
	}
	var out map[string]string
	err := s.locker.WithTxLock(ctx, orgID, projectID, func(ctx context.Context) error {
		files, err := s.gitClient.RestoreRequirementsSnapshot(ctx, orgID, projectID, turnID)
		if err != nil {
			return fmt.Errorf("restore snapshot: %w", err)
		}
		// Snapshot used once; drop it so we don't accumulate stale blobs.
		if err := s.gitClient.DeleteRequirementsSnapshot(ctx, orgID, projectID, turnID); err != nil {
			slog.WarnContext(ctx, "snapshot delete failed (non-fatal)",
				"project", projectID, "turn", turnID, "error", err)
		}
		out = files
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSessionBaselineFile fetches `filename`'s content as captured in the
// session baseline snapshot. Read-only — no dir lock taken.
func (s *requirementsChatService) GetSessionBaselineFile(ctx context.Context, orgID, projectID, baselineID, filename string) (*gitservice.RequirementsSnapshotFile, error) {
	if baselineID == "" {
		return nil, fmt.Errorf("baselineID is required")
	}
	if filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	return s.gitClient.ReadRequirementsSnapshotFile(ctx, orgID, projectID, baselineID, filename)
}

// DropSessionBaseline deletes the baseline snapshot blob. Idempotent.
// Called by the console after Accept clears the last modified file.
func (s *requirementsChatService) DropSessionBaseline(ctx context.Context, orgID, projectID, baselineID string) error {
	if baselineID == "" {
		return fmt.Errorf("baselineID is required")
	}
	return s.gitClient.DeleteRequirementsSnapshot(ctx, orgID, projectID, baselineID)
}

// RevertFileToBaseline rewrites a single requirement file to the content
// captured in the baseline. If the file didn't exist at baseline (the
// agent created it post-baseline) the working-tree file is deleted.
// Held under the same dir lock as chat turns / manual PUTs so concurrent
// writers see a consistent state.
func (s *requirementsChatService) RevertFileToBaseline(ctx context.Context, orgID, projectID, baselineID, filename string) error {
	if baselineID == "" {
		return fmt.Errorf("baselineID is required")
	}
	if filename == "" {
		return fmt.Errorf("filename is required")
	}
	file, err := s.gitClient.ReadRequirementsSnapshotFile(ctx, orgID, projectID, baselineID, filename)
	if err != nil {
		return fmt.Errorf("read baseline file: %w", err)
	}
	return s.locker.WithTxLock(ctx, orgID, projectID, func(ctx context.Context) error {
		if !file.Existed {
			// File did not exist at baseline → delete to match the baseline
			// state. The protected main file (`requirements.md`) can't be
			// deleted, so the closest reachable approximation is to truncate
			// to empty content; the user keeps a draft to edit instead of
			// being stuck with the chat-generated content.
			if filename == RequirementsMainFile {
				if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, filename, ""); err != nil {
					return fmt.Errorf("clear %s: %w", filename, err)
				}
				return nil
			}
			if err := s.store.DeleteRequirementFile(ctx, orgID, projectID, filename); err != nil {
				if errors.Is(err, gitservice.ErrArtifactNotFound) {
					// Already gone — treat as success.
					return nil
				}
				return fmt.Errorf("delete %s: %w", filename, err)
			}
			return nil
		}
		if _, err := s.store.WriteRequirementFile(ctx, orgID, projectID, filename, file.Content); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
		return nil
	})
}

// ----- Helpers -----

// filterScope returns the subset of `all` keyed by `scope`. Empty scope
// means "everything".
func filterScope(all map[string]string, scope []string) map[string]string {
	if len(scope) == 0 {
		return all
	}
	out := make(map[string]string, len(scope))
	for _, name := range scope {
		if v, ok := all[name]; ok {
			out[name] = v
		}
	}
	return out
}

func fallbackMode(m string) string {
	if m == "ask" {
		return "ask"
	}
	return "edit"
}

func randomID() string {
	var buf [10]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
