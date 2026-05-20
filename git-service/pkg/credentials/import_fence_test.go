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

// TestOpenBaoImportFence walks every Go file under git-service/ and fails the
// build if any file outside `pkg/credentials/` imports an OpenBao or HashiCorp
// Vault SDK package. This is the architectural enforcement of the §6.5
// boundary: the OpenBaoStore wrapper is the only OpenBao access point, and
// per-org isolation rests on no other package being able to talk to OpenBao
// directly.
//
// Phase 2 will swap the placeholder for a real implementation; this test
// guards the boundary as that implementation lands.
func TestOpenBaoImportFence(t *testing.T) {
	// Walk up from this file's location to the git-service root so the test
	// is robust to where `go test` is invoked from.
	root := findGitServiceRoot(t)
	allowedPrefix := filepath.Join("pkg", "credentials") + string(filepath.Separator)

	bannedPrefixes := []string{
		`"github.com/openbao/`,
		`"github.com/hashicorp/vault`,
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip vendored or generated trees.
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
		if strings.HasPrefix(rel, allowedPrefix) {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			// Don't fail the fence on unparseable files — that's a syntax
			// problem, not an import-boundary violation.
			return nil
		}
		for _, imp := range f.Imports {
			for _, banned := range bannedPrefixes {
				if strings.HasPrefix(imp.Path.Value, banned) {
					t.Errorf("openbao import fence violated: %s imports %s — only pkg/credentials/ may import the OpenBao/Vault SDK",
						rel, imp.Path.Value)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func findGitServiceRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		// git-service module root holds go.mod + the pkg/credentials dir.
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "pkg", "credentials")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate git-service root from %s", wd)
		}
		dir = parent
	}
}
