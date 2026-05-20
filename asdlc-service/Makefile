.PHONY: build test check-no-legacy-creds gen-oc-client gen-oc-client-check gen-mocks

build:
	go build ./...

test: check-no-legacy-creds
	go test ./...

# Pinned spec version — bump this and re-run `make gen-oc-client` when the
# upstream OC API changes. Keep aligned with the OC build deployed by the
# deployments-v2/wso2cloud-deployment submodule. Matches agent-manager.
OC_SPEC_VERSION ?= v1.0.1-hotfix.1
OC_SPEC_URL     ?= https://raw.githubusercontent.com/openchoreo/openchoreo/$(OC_SPEC_VERSION)/openapi/openchoreo-api.yaml
OAPI_CODEGEN    ?= go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0

# Regenerate the OpenChoreo client from the pinned upstream spec.
# Generated files in clients/openchoreo/gen/ are checked in; this target
# overwrites them and is the only supported way to modify them.
gen-oc-client:
	@echo "Fetching OpenChoreo spec $(OC_SPEC_VERSION)"
	@curl -fsSL "$(OC_SPEC_URL)" -o /tmp/openchoreo-api.yaml
	@echo "Generating types"
	@$(OAPI_CODEGEN) -config clients/openchoreo/gen/oapi-codegen.yaml /tmp/openchoreo-api.yaml
	@echo "Generating client"
	@$(OAPI_CODEGEN) -config clients/openchoreo/gen/oapi-codegen-client.yaml /tmp/openchoreo-api.yaml
	@rm /tmp/openchoreo-api.yaml
	@gofmt -w clients/openchoreo/gen
	@echo "OpenChoreo client generated successfully"

# Regenerate moq fakes for the OC client interfaces. Drops files into
# clients/openchoreo/mocks/ — only commit them once a test actually uses
# them, otherwise they're dead code (the `//go:generate` directives mean
# `go generate ./clients/openchoreo/...` always reproduces them).
gen-mocks:
	@echo "Regenerating moq fakes"
	@go generate ./clients/openchoreo/...
	@echo "Mocks regenerated under clients/openchoreo/mocks/"

# CI sync check: regenerate, then fail if any tracked *.gen.go drifted.
# Uses `git diff --exit-code` so untracked files (e.g. a freshly-cut branch
# that hasn't committed gen/ yet) don't trip the check — only changes to
# already-tracked output do.
gen-oc-client-check: gen-oc-client
	@if ! git diff --exit-code --quiet -- 'clients/openchoreo/gen/*.gen.go'; then \
		git diff --color -- 'clients/openchoreo/gen/*.gen.go' | cat; \
		echo ""; \
		echo "clients/openchoreo/gen/*.gen.go is out of sync with $(OC_SPEC_VERSION)."; \
		echo "Run 'make gen-oc-client' locally and commit the changes."; \
		exit 1; \
	fi

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
	  | grep -v '/clients/openchoreo/gen/' \
	  | grep -vE ':[[:space:]]*//' \
	  || true); \
	if [ -n "$$matches" ]; then \
		echo "legacy credential / cleanup surface detected (see phase2.md §1.10 / §15.4):"; \
		echo "$$matches"; \
		exit 1; \
	fi
