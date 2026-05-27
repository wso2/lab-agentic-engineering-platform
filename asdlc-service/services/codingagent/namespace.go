package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// OrgBaseNamespace returns the org's base namespace name matching
// wso2cloud's `ou.util.GenerateNamespaceName` (= the same shape
// secret-manager-api derives server-side from the JWT's orgUUID claim):
//
//	wc-<first-8-chars-of-cleaned-uuid>-<8-char-sha256-hex>
//
// SM-API uses this as the namespace it writes SecretReference CRs into;
// the BFF must compute the same value when reconstructing the Vault
// path for ExternalSecret.spec.data[].remoteRef.key (Vault path shape:
// `<vaultPathPrefix>/<orgNS>/<secretRefName>`). Deterministic — same
// orgUUID always produces the same name.
func OrgBaseNamespace(orgUUID string) string {
	clean := strings.ReplaceAll(orgUUID, "-", "")
	prefix := clean
	if len(clean) > 8 {
		prefix = clean[:8]
	}
	hash := sha256.Sum256([]byte(orgUUID))
	salt := hex.EncodeToString(hash[:])[:8]
	return fmt.Sprintf("wc-%s-%s", strings.ToLower(prefix), salt)
}

// RemoteWorkerNamespace returns the per-org remote-worker namespace
// name = OrgBaseNamespace(orgUUID) + "-remote-worker".
//
// The `-remote-worker` suffix is the asdlc fork — wso2cloud's existing
// shapes all use `-development`, `-staging`, `-production`. The
// env-less name is intentional: a coding-agent task isn't bound to a
// user-facing env (per ADR-0001). When wso2cloud-deployement-main
// pushes a renamed shape we follow.
func RemoteWorkerNamespace(orgUUID string) string {
	return OrgBaseNamespace(orgUUID) + "-remote-worker"
}
