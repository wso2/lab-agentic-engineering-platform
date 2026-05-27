package services

import (
	"context"
	"fmt"
	"time"

	"github.com/wso2/asdlc/asdlc-service/internal/credentials"
)

// RefreshResponse is what the workspace credential helper consumes. The
// shape is identical for Phase 0 (long-lived PAT) and Phase 2 (short-lived
// App tokens) — only the ExpiresAt differs.
type RefreshResponse struct {
	Token     string               `json:"token"`
	ExpiresAt time.Time            `json:"expiresAt"`
	Identity  credentials.Identity `json:"identity"`
	TaskID    string               `json:"taskId"`
}

// CredentialsRefreshService returns a fresh GitHub token + identity for the
// task named in a verified per-task Task JWT.
//
// The Task JWT is verified at the controller layer via JWKS-backed RS256
// (jwtassertion). Its claims (taskID, ocOrgID) are trusted because the
// signature originates from the BFF's RSA private key. There is no
// callback into the BFF anymore — the JWT itself carries all the org
// context needed.
type CredentialsRefreshService interface {
	Refresh(ctx context.Context, taskID, ocOrgID string) (*RefreshResponse, error)
}

type credentialsRefreshService struct {
	resolver credentials.Resolver
}

// NewCredentialsRefreshService constructs the service.
func NewCredentialsRefreshService(resolver credentials.Resolver) CredentialsRefreshService {
	return &credentialsRefreshService{resolver: resolver}
}

func (s *credentialsRefreshService) Refresh(ctx context.Context, taskID, ocOrgID string) (*RefreshResponse, error) {
	cred, err := s.resolver.Resolve(ctx, ocOrgID)
	if err != nil {
		return nil, fmt.Errorf("resolve credential: %w", err)
	}
	token, expiresAt, err := cred.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	return &RefreshResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		Identity:  cred.Identity(),
		TaskID:    taskID,
	}, nil
}
