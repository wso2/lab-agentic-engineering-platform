package services

import (
	"encoding/json"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/wso2/asdlc/asdlc-service/models"
)

// normalizeDesignJSON returns a canonical-form encoding of the design.
// Goal: byte-stable across LLM regenerations whose only differences are
// whitespace, key ordering, scalar style, or YAML anchor expansion. With
// canonical bytes, git-service's strings.TrimSpace equality dedup correctly
// returns "unchanged" instead of bumping a tag for noise.
//
// Per design doc §10:
//   - Recursive alphabetical key order at every nesting level.
//   - HTTP status-code keys coerced to string (200 → "200").
//   - x-* extensions preserved verbatim.
//   - Empty `required`, `parameters`, `tags`, `security` arrays omitted.
//   - YAML anchors / aliases dealiased (semantic content compared).
//   - Block style preserved on multi-line strings (yaml.v3 default does this).
//
// Idempotent: re-normalizing canonical content is a no-op.
func normalizeDesignJSON(raw []byte) ([]byte, error) {
	// Unmarshal into a flexible structure. We use json.RawMessage on the
	// outer envelope because we only care about transforming the OpenAPI
	// strings inside components — the design.json shape itself is already
	// stable (struct-tag-deterministic).
	var df struct {
		Overview     string                   `json:"overview"`
		Requirements []string                 `json:"requirements"`
		Components   []models.DesignComponent `json:"components"`
		SourceSpec   string                   `json:"sourceSpec,omitempty"`
	}
	if err := json.Unmarshal(raw, &df); err != nil {
		return nil, fmt.Errorf("normalize: parse design json: %w", err)
	}

	for i := range df.Components {
		if df.Components[i].OpenAPISpec == "" {
			continue
		}
		canonical, err := normalizeOpenAPIYAML(df.Components[i].OpenAPISpec)
		if err != nil {
			// Don't fail the whole save on a single broken spec. The
			// architect validator already gated on parse-ability before
			// emitting data-finish, so this should be rare; if it does
			// happen, leaving the original bytes through is safer than
			// dropping data.
			continue
		}
		df.Components[i].OpenAPISpec = canonical
	}

	out, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("normalize: encode design json: %w", err)
	}
	return out, nil
}

// normalizeOpenAPIYAML applies the rules from design doc §10 to a single
// OpenAPI spec.
func normalizeOpenAPIYAML(in string) (string, error) {
	var raw any
	if err := yaml.Unmarshal([]byte(in), &raw); err != nil {
		return "", err
	}
	cleaned := canonicalize(raw)
	out, err := yaml.Marshal(cleaned)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// emptyDropKeys lists keys whose value is dropped from the parent mapping
// when the value is an empty array. These are the OpenAPI "noise" keys —
// authors emit them inconsistently, but they carry no semantic content.
var emptyDropKeys = map[string]struct{}{
	"required":   {},
	"parameters": {},
	"tags":       {},
	"security":   {},
}

// canonicalize walks the parsed-YAML tree and returns a tree where:
//   - All maps are map[string]any (status-code int keys coerced to "200" form).
//   - Pairs whose value is an empty array AND whose key is in emptyDropKeys are removed.
//
// yaml.v3's Marshal sorts map keys alphabetically by default, so we don't
// need an explicit sort pass — the marshaler does it for us.
func canonicalize(node any) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			cv := canonicalize(v)
			if shouldDrop(k, cv) {
				continue
			}
			out[k] = cv
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			ks := keyToString(k)
			cv := canonicalize(v)
			if shouldDrop(ks, cv) {
				continue
			}
			out[ks] = cv
		}
		return out
	case []any:
		// Preserve element order (semantic in OpenAPI for tags/parameters/etc.).
		out := make([]any, len(n))
		for i, v := range n {
			out[i] = canonicalize(v)
		}
		return out
	default:
		return n
	}
}

func shouldDrop(key string, val any) bool {
	if _, ok := emptyDropKeys[key]; !ok {
		return false
	}
	arr, ok := val.([]any)
	return ok && len(arr) == 0
}

func keyToString(k any) string {
	switch v := k.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		// Only happens for integer-looking floats; OpenAPI status codes are integers.
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", v)
	}
}
