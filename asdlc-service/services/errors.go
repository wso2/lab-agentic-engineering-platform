package services

import "errors"

var (
	ErrProjectNotFound     = errors.New("project not found")
	ErrComponentNotFound   = errors.New("component not found")
	ErrComponentNotService = errors.New("component is not a service")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrSpecNotFound        = errors.New("spec not found")
	ErrSpecEmpty           = errors.New("spec content is empty")
	ErrSpecNotApproved     = errors.New("spec must be saved (tagged) before generating a design")
	ErrDesignNotFound      = errors.New("design not found")
	ErrDesignNotApproved   = errors.New("design must be saved (tagged) before generating tasks")
	ErrTasksInFlight       = errors.New("tasks already in progress; cannot regenerate")
	ErrBuildNotFound       = errors.New("build not found")
	ErrDeploymentFailed    = errors.New("deployment failed")
	ErrLogsUnavailable     = errors.New("observability service not configured")
	ErrTaskNotFound        = errors.New("task not found")

	// Folded in from git-service after WS0.1.g.
	ErrRepoNotFound      = errors.New("repository not found")
	ErrRepoAlreadyExists = errors.New("repository already exists for this project")
	ErrRepoNotReady      = errors.New("repository is not ready")
	ErrAuthFailed        = errors.New("git authentication failed")
	ErrPushConflict      = errors.New("push rejected")
	ErrFileNotFound      = errors.New("file not found")
	ErrTagNotFound       = errors.New("tag not found")
	ErrTagAlreadyExists  = errors.New("tag already exists")
)
