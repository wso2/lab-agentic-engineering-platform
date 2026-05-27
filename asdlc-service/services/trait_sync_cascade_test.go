package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/mocks"
	"github.com/wso2/asdlc/asdlc-service/models"
)

// Tests exercising the OC-touching paths of TraitSyncService that don't
// require an artifact store (i.e. don't read design.md). Pure design
// read paths are tested implicitly via the E2E console flow + the unit
// tests for DesiredAPIConfigurationTrait / componentNameFromDesignPath.

// TestTraitSync_DeleteCascade_HappyPath — calls componentClient.DeleteComponent
// exactly once with the scoped (org, project, componentName) tuple and
// returns nil. Audit logging is fire-and-forget — we don't assert on
// it here because slog goes to a global sink; the cluster smoke test
// in the prior Playwright run is the integration check.
func TestTraitSync_DeleteCascade_HappyPath(t *testing.T) {
	calls := 0
	mock := &mocks.ComponentClientMock{
		DeleteComponentFunc: func(ctx context.Context, orgName, projectName, componentName string) error {
			calls++
			if orgName != "org-1" || projectName != "proj-1" || componentName != "comp-x" {
				t.Errorf("unexpected delete args: %s/%s/%s", orgName, projectName, componentName)
			}
			return nil
		},
	}
	svc := NewTraitSyncService(mock, nil)
	if err := svc.DeleteComponentCascade(context.Background(), "org-1", "proj-1", "comp-x"); err != nil {
		t.Fatalf("DeleteComponentCascade: %v", err)
	}
	if calls != 1 {
		t.Fatalf("want DeleteComponent calls=1, got %d", calls)
	}
}

// TestTraitSync_DeleteCascade_PropagatesError — when OC's DeleteComponent
// returns an error (network, 5xx after exhaustion), the cascade surfaces
// it wrapped with "trait_sync: delete component". The caller
// (designService.DeleteComponent) treats this as best-effort and logs
// but does not propagate to the user.
func TestTraitSync_DeleteCascade_PropagatesError(t *testing.T) {
	mock := &mocks.ComponentClientMock{
		DeleteComponentFunc: func(ctx context.Context, orgName, projectName, componentName string) error {
			return errors.New("simulated OC failure")
		},
	}
	svc := NewTraitSyncService(mock, nil)
	err := svc.DeleteComponentCascade(context.Background(), "org", "proj", "comp")
	if err == nil {
		t.Fatal("want error from DeleteComponentCascade, got nil")
	}
	if !strings.Contains(err.Error(), "delete component") {
		t.Errorf("error should wrap with trait_sync delete prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated OC failure") {
		t.Errorf("error should preserve underlying message: %v", err)
	}
}

// TestTraitSync_DeleteCascade_EmptyArgsRejected — defensive check: the
// orchestration layer must catch empty IDs before the OC call, so a
// missing path param never reaches the cluster.
func TestTraitSync_DeleteCascade_EmptyArgsRejected(t *testing.T) {
	mock := &mocks.ComponentClientMock{
		DeleteComponentFunc: func(ctx context.Context, orgName, projectName, componentName string) error {
			t.Fatal("DeleteComponent must not be called with empty args")
			return nil
		},
	}
	svc := NewTraitSyncService(mock, nil)
	cases := []struct {
		name        string
		org, p, c   string
	}{
		{"no org", "", "p", "c"},
		{"no project", "o", "", "c"},
		{"no component", "o", "p", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.DeleteComponentCascade(context.Background(), tc.org, tc.p, tc.c)
			if err == nil {
				t.Fatalf("want error for case %s, got nil", tc.name)
			}
		})
	}
}

// TestSyncComponentTraits_RejectsEmptyArgs — same defensive contract as
// DeleteCascade above. Empty IDs should never trigger OC reads.
func TestSyncComponentTraits_RejectsEmptyArgs(t *testing.T) {
	mock := &mocks.ComponentClientMock{
		UpdateComponentTraitsFunc: func(ctx context.Context, orgName, projectName, componentName string, traits []models.ComponentTrait) error {
			t.Fatal("UpdateComponentTraits must not be called with empty args")
			return nil
		},
	}
	svc := NewTraitSyncService(mock, nil)
	if err := svc.SyncComponentTraits(context.Background(), "", "p", "c"); err == nil {
		t.Fatal("want error, got nil")
	}
}
