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
)
