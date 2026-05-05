package services

import (
	"errors"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/models"
)

func TestApplyTaskEventHappyPath(t *testing.T) {
	cases := []struct {
		from  models.TaskStatus
		event TaskEvent
		want  models.TaskStatus
	}{
		{models.TaskStatusPending, TaskEventDispatchSuccess, models.TaskStatusInProgress},
		{models.TaskStatusInProgress, TaskEventPRReady, models.TaskStatusReadyForReview},
		{models.TaskStatusReadyForReview, TaskEventPRMerged, models.TaskStatusMerged},
		{models.TaskStatusReadyForReview, TaskEventPRRejected, models.TaskStatusRejected},
		{models.TaskStatusInProgress, TaskEventPRRejected, models.TaskStatusRejected},
		{models.TaskStatusMerged, TaskEventPushMatched, models.TaskStatusBuilding},
		{models.TaskStatusBuilding, TaskEventBuildSucceeded, models.TaskStatusDeployed},
		{models.TaskStatusBuilding, TaskEventBuildFailed, models.TaskStatusFailed},
	}
	for _, c := range cases {
		got, err := ApplyTaskEvent(c.from, c.event)
		if err != nil {
			t.Errorf("from=%s event=%s: unexpected error %v", c.from, c.event, err)
		}
		if got != c.want {
			t.Errorf("from=%s event=%s: got %s, want %s", c.from, c.event, got, c.want)
		}
	}
}

func TestApplyTaskEventTerminalAbsorbsLateEvents(t *testing.T) {
	for _, term := range []models.TaskStatus{
		models.TaskStatusDeployed,
		models.TaskStatusRejected,
		models.TaskStatusFailed,
	} {
		_, err := ApplyTaskEvent(term, TaskEventPRMerged)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("terminal %s: expected ErrInvalidTransition, got %v", term, err)
		}
	}
}

func TestApplyTaskEventRefusesUnknownTransition(t *testing.T) {
	_, err := ApplyTaskEvent(models.TaskStatusPending, TaskEventBuildSucceeded)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}
