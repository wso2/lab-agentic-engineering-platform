package webhook

import (
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
	"github.com/wso2/asdlc/asdlc-service/services"
)

// classifyRun unit tests — pure function, no DB. The retry-budget +
// projector dispatch are exercised in integration tests against the
// docker-compose stack (M7 verification).

func TestClassifyRun_Nil(t *testing.T) {
	event, msg, auth := classifyRun(nil)
	if event != "" || msg != "" || auth {
		t.Fatalf("nil run should yield no event")
	}
}

func TestClassifyRun_Pending(t *testing.T) {
	run := &models.WorkflowRun{Status: "Pending"}
	event, _, auth := classifyRun(run)
	if event != "" || auth {
		t.Fatalf("Pending should yield no event, got %q auth=%v", event, auth)
	}
}

func TestClassifyRun_Succeeded(t *testing.T) {
	run := &models.WorkflowRun{Status: "WorkflowSucceeded"}
	event, _, auth := classifyRun(run)
	if event != services.TaskEventBuildSucceeded || auth {
		t.Fatalf("expected BuildSucceeded, got %q auth=%v", event, auth)
	}
}

func TestClassifyRun_Failed_NoTaskOutputs(t *testing.T) {
	run := &models.WorkflowRun{Status: "WorkflowFailed"}
	event, msg, auth := classifyRun(run)
	if event != services.TaskEventBuildFailed || auth {
		t.Fatalf("plain failure should be terminal failed, got %q auth=%v", event, auth)
	}
	if msg == "" {
		t.Fatalf("expected non-empty error message on terminal failure")
	}
}

func TestClassifyRun_Failed_AuthMarkerInOutput(t *testing.T) {
	cases := []struct {
		name string
		run  *models.WorkflowRun
	}{
		{
			name: "fatal authentication failed",
			run: &models.WorkflowRun{
				Status: "WorkflowFailed",
				Tasks: []models.WorkflowRunTask{
					{Name: "checkout-source", Outputs: map[string]string{"error": "fatal: Authentication failed for 'https://github.com/foo/bar.git/'"}},
				},
			},
		},
		{
			name: "401 status surface",
			run: &models.WorkflowRun{
				Status: "WorkflowFailed",
				Tasks: []models.WorkflowRunTask{
					{Name: "checkout-source", Outputs: map[string]string{"error": "the requested URL returned error: 401"}},
				},
			},
		},
		{
			name: "could not read username",
			run: &models.WorkflowRun{
				Status: "WorkflowFailed",
				Tasks: []models.WorkflowRunTask{
					{Name: "checkout-source", Outputs: map[string]string{"stderr": "fatal: could not read Username for 'https://github.com'"}},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, _, auth := classifyRun(tc.run)
			if event != "" {
				t.Fatalf("auth-failure run should NOT yield a terminal event, got %q", event)
			}
			if !auth {
				t.Fatalf("auth-failure run should set authFailure=true")
			}
		})
	}
}

func TestClassifyRun_Failed_NonAuthMarker_NotRetried(t *testing.T) {
	// A failure that doesn't match any auth marker stays terminal.
	run := &models.WorkflowRun{
		Status: "WorkflowFailed",
		Tasks: []models.WorkflowRunTask{
			{Name: "build-image", Outputs: map[string]string{"error": "ENOSPC: no space left on device"}},
		},
	}
	event, _, auth := classifyRun(run)
	if event != services.TaskEventBuildFailed {
		t.Fatalf("non-auth failure should still be terminal failed, got %q", event)
	}
	if auth {
		t.Fatalf("non-auth failure must not set authFailure")
	}
}
