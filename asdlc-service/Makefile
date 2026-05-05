.PHONY: build test check-no-legacy-creds

build:
	go build ./...

test: check-no-legacy-creds
	go test ./...

# Phase 2 PR C — fail the build if the legacy GetCredentials bridge or
# CreateGitSecret resurfaces, or if any code references the retired
# 'platform-pat' kind. Skips test files and comment-only references
# (lines whose first non-whitespace char after the path:line: is `//`).
#
# Phase 2 PR D — also asserts EnvSecretProvider and tier_gate stay
# deleted (per phase2.md §15.4 PR D cleanup checklist). Both were
# removed in PR B / PR A and must not resurface.
check-no-legacy-creds:
	@matches=$$(grep -rEn 'gitservice\.GetCredentials|\bCreateGitSecret\b|"platform-pat"|\bEnvSecretProvider\b|\btier_gate\b' --include='*.go' . 2>/dev/null \
	  | grep -v '_test\.go' \
	  | grep -vE ':[[:space:]]*//' \
	  || true); \
	if [ -n "$$matches" ]; then \
		echo "legacy credential / cleanup surface detected (see phase2.md §1.10 / §15.4):"; \
		echo "$$matches"; \
		exit 1; \
	fi
