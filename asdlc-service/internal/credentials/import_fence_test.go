package credentials_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fenceRule is one row of the table-driven import fence. Each row says:
// "no Go file outside `allowedPrefix` may import any package whose path
// starts with one of `bannedImportPrefixes`".
//
// The fence runs once over the entire asdlc-service tree per row, so a
// new rule adds one row, not a new walk.
type fenceRule struct {
	name                 string
	allowedPrefix        string   // path prefix (slash-joined) under module root; "" disallows everywhere
	bannedImportPrefixes []string // quoted import paths, including the opening quote
	reason               string   // included in failure message — explain why the edge is forbidden
}

// rules enumerates every MUST-NOT import edge from
// docs/oc-refactor/10-target-architecture.md §0.3 plus the
// existing OpenBao fence. New rows MUST cite the §-reference (or an
// ADR) in `reason`.
var rules = []fenceRule{
	{
		name:                 "openbao-sdk",
		allowedPrefix:        filepath.Join("internal", "credentials") + string(filepath.Separator),
		bannedImportPrefixes: []string{`"github.com/openbao/`, `"github.com/hashicorp/vault`},
		reason: "OpenBao/Vault SDK access is confined to internal/credentials/ (the OpenBaoStore wrapper). " +
			"Per-org isolation in §6.5 rests on no other package being able to talk to OpenBao directly.",
	},
	{
		name:                 "openchoreo-gen-direct",
		allowedPrefix:        filepath.Join("clients", "openchoreo") + string(filepath.Separator),
		bannedImportPrefixes: []string{`"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"`},
		reason: "The oapi-codegen layer (clients/openchoreo/gen) is an implementation detail. " +
			"Services + controllers go through the typed wrappers in clients/openchoreo/ so a " +
			"schema regen doesn't ripple across the BFF.",
	},
}

func TestImportFences(t *testing.T) {
	root := findModuleRoot(t)
	fset := token.NewFileSet()

	for _, rule := range rules {
		t.Run(rule.name, func(t *testing.T) {
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					name := d.Name()
					if name == "vendor" || name == "node_modules" || name == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") {
					return nil
				}
				rel, rerr := filepath.Rel(root, path)
				if rerr != nil {
					return rerr
				}
				if rule.allowedPrefix != "" && strings.HasPrefix(rel, rule.allowedPrefix) {
					return nil
				}

				f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
				if perr != nil {
					// Don't fail the fence on unparseable files — that's a
					// syntax problem, not an import-boundary violation.
					return nil
				}
				for _, imp := range f.Imports {
					for _, banned := range rule.bannedImportPrefixes {
						if strings.HasPrefix(imp.Path.Value, banned) {
							t.Errorf("[%s] %s imports %s — violation.\n  reason: %s",
								rule.name, rel, imp.Path.Value, rule.reason)
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
		})
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		// asdlc-service module root holds go.mod + internal/credentials.
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "internal", "credentials")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate asdlc-service root from %s", wd)
		}
		dir = parent
	}
}
