package openchoreo

import (
	"encoding/json"
	"time"

	"github.com/wso2/asdlc/asdlc-service/clients/openchoreo/gen"
)

// structToMap round-trips v through JSON into a `map[string]interface{}`.
// The gen ClusterComponentTypeSpec has many inline anonymous structs that
// would be tedious to unpack field-by-field; JSON-mirror keeps the wire
// shape identical with one helper. Returns the parsed map or an error.
func structToMap(v any) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// derefStr returns *s or "" if s is nil. The generated OC types
// (gen.ObjectMeta.Uid, gen.ObjectMeta.Namespace, …) are pointer-everywhere
// per oapi-codegen's `omitempty`-default rule, so unwrap helpers DRY the
// per-method conversion. Matches the role agent-manager's
// `utils.StrPointerAsStr` plays.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefTimeRFC3339 returns t formatted in RFC3339 UTC, or "" if t is nil.
// models.Project.CreatedAt is a string; OC surfaces *time.Time.
func derefTimeRFC3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// annotation reads key from a pointer-to-map without panicking on nil.
// Annotations are `*map[string]string` on every gen ObjectMeta.
func annotation(ann *map[string]string, key string) string {
	if ann == nil {
		return ""
	}
	return (*ann)[key]
}

// label reads key from a pointer-to-map without panicking on nil. Labels
// are `*map[string]string` on every gen ObjectMeta.
func label(lbls *map[string]string, key string) string {
	if lbls == nil {
		return ""
	}
	return (*lbls)[key]
}

// latestConditionReason returns the Reason of the last entry in conds, or "".
// Mirrors the existing oc_types.go behaviour the hand-rolled clients used,
// against the gen Condition shape (Type/Status/Reason/Message/…).
func latestConditionReason(conds *[]gen.Condition) string {
	if conds == nil || len(*conds) == 0 {
		return ""
	}
	c := *conds
	return c[len(c)-1].Reason
}
