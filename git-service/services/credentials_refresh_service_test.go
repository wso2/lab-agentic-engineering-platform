package services

import (
	"context"
	"testing"
	"time"

	"github.com/wso2/asdlc/git-service/pkg/credentials"
)

// (fakeResolver and fakeCred re-used from build_credentials_service_test.go)

func TestRefresh_Happy(t *testing.T) {
	expiry := time.Now().Add(time.Hour)
	res := &fakeResolver{cred: &fakeCred{token: "ghs_abc", exp: expiry}}

	svc := NewCredentialsRefreshService(res)
	resp, err := svc.Refresh(context.Background(), "task-1", "default")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if resp.Token != "ghs_abc" {
		t.Errorf("token = %q; want ghs_abc", resp.Token)
	}
	if resp.TaskID != "task-1" {
		t.Errorf("taskId echo = %q; want task-1", resp.TaskID)
	}
}

// Ensure fakeCred matches the credentials.Credential interface.
var _ credentials.Credential = (*fakeCred)(nil)
