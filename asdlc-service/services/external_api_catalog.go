package services

// ExternalAPICatalog resolves architect-declared dependent-API intents
// (keyed by component-style name like "employee-api") into concrete URLs.
// The architect prompt no longer carries hardcoded URL strings — it
// declares the intent by name only, and the BFF supplies the URL from
// this catalog at design-load time.
//
// Phase 1 scope: a single in-process map seeded from BFF config. Phase 2+
// can swap this for a DB-backed table without changing the call sites.
type ExternalAPICatalog struct {
	entries map[string]ExternalAPIEntry
}

// ExternalAPIEntry is one row of the catalog.
type ExternalAPIEntry struct {
	URL            string
	Authentication string // "none" | "bearer" | "api-key" — used when the architect didn't specify
}

// NewExternalAPICatalog builds a catalog from the entries map. nil/empty
// map yields a catalog where Lookup always returns "" — useful in tests
// that don't care about Secret Santa-style intents.
func NewExternalAPICatalog(entries map[string]ExternalAPIEntry) *ExternalAPICatalog {
	if entries == nil {
		entries = map[string]ExternalAPIEntry{}
	}
	return &ExternalAPICatalog{entries: entries}
}

// DefaultExternalAPICatalog returns the platform's shipped seed list. In
// Phase 1 this is the single "employee-api" entry that powers the
// Secret Santa scenario. Add more entries as more in-tree scenarios get
// canonicalised.
func DefaultExternalAPICatalog() *ExternalAPICatalog {
	return NewExternalAPICatalog(map[string]ExternalAPIEntry{
		"employee-api": {
			URL:            "http://development-default.openchoreoapis.localhost:19080/employee-app-employee-api-http/employees",
			Authentication: "none",
		},
	})
}

// Lookup returns the URL + default auth method for the given intent name.
// Empty URL means "no catalog entry" — the caller falls back to whatever
// the architect emitted (if anything) and may surface a validation error.
func (c *ExternalAPICatalog) Lookup(name string) ExternalAPIEntry {
	if c == nil {
		return ExternalAPIEntry{}
	}
	return c.entries[name]
}
