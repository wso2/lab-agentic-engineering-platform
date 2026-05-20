package services

import "errors"

var (
	ErrRepoNotFound      = errors.New("repository not found")
	ErrRepoAlreadyExists = errors.New("repository already exists for this project")
	ErrRepoNotReady      = errors.New("repository is not ready")
	ErrAuthFailed        = errors.New("git authentication failed")
	ErrPushConflict      = errors.New("push rejected")
	ErrFileNotFound      = errors.New("file not found")
	ErrTagNotFound       = errors.New("tag not found")
	ErrTagAlreadyExists  = errors.New("tag already exists")
)
