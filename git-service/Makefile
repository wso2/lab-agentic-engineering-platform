.PHONY: build test check-no-legacy-creds

build:
	go build ./...

test: check-no-legacy-creds
	go test ./...

# Phase 2 PR C — fail the build if the legacy GetCredentials bridge or
# CreateGitSecret resurfaces. Skips test files and comment-only references.
# Match `\bGetCredentials\b` strictly (won't match GetCredentialIdentity,
# GetCredentialProjection, etc.).
check-no-legacy-creds:
	@matches=$$(grep -rEn '\bGetCredentials\b|\bCreateGitSecret\b' --include='*.go' . 2>/dev/null \
	  | grep -v '_test\.go' \
	  | grep -vE ':[[:space:]]*//' \
	  || true); \
	if [ -n "$$matches" ]; then \
		echo "legacy credential surface detected (see phase2.md §1.10):"; \
		echo "$$matches"; \
		exit 1; \
	fi
