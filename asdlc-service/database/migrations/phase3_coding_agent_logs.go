// Phase 3 — coding_agent_logs sidecar table.
//
// Captures the final pod-log tail for new-path (cluster-gateway-proxy)
// coding-agent dispatches when the Job hits terminal state. Read by
// `progress_service.GetAgentProgress` once the Job is past TTL so the
// console can still surface diagnostics (the legacy path used
// OpenChoreo's Observer + OpenSearch; the new dispatch NS
// (`wc-…-remote-worker`) doesn't match Observer's hardcoded
// `workflows-<…>` filter, so the BFF tails `pods/log` itself).
//
// Sidecar rather than a column on `component_tasks`: the parent table
// holds only small hot fields and is read on every list / status /
// dispatch path; appending a TEXT TOAST'd blob there would force
// detoasting whenever ORM SELECT-* paths run. Mirrors the existing
// `webhook_payloads` ↔ `webhook_deliveries` split.
package migrations

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func RunPhase3CodingAgentLogs(ctx context.Context, db *gorm.DB) error {
	stmt := `DO $$ BEGIN
		   IF EXISTS (SELECT FROM information_schema.tables
		              WHERE table_schema='public' AND table_name='component_tasks') THEN
		     CREATE TABLE IF NOT EXISTS coding_agent_logs (
		       task_id      UUID         NOT NULL REFERENCES component_tasks(id) ON DELETE CASCADE,
		       run_name     TEXT         NOT NULL,
		       final_phase  TEXT         NOT NULL,
		       captured_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
		       log_text     TEXT         NOT NULL,
		       size_bytes   BIGINT       NOT NULL,
		       PRIMARY KEY (task_id, run_name)
		     );
		     CREATE INDEX IF NOT EXISTS idx_coding_agent_logs_task_id
		       ON coding_agent_logs(task_id);
		   END IF;
		 END $$`
	if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return fmt.Errorf("phase3_coding_agent_logs: %w", err)
	}
	return nil
}
