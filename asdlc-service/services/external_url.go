package services

import "strings"

// NormalizeExternalURL ensures a URL or path ends with a single trailing
// slash. The AP router 404s on bare context paths without one — see
// gotcha C3 in deployments/POC-API-PLATFORM.md. ALL code that reads
// `ReleaseBinding.status.endpoints[].externalURLs.http.path` (or the full
// composed URL) MUST pass it through this helper before handing it to a
// caller or rendering it in the console.
//
// Empty input returns "" unchanged (callers handle that as "no URL yet").
func NormalizeExternalURL(raw string) string {
	if raw == "" {
		return ""
	}
	// Strip any trailing slashes then append exactly one. Cheap, allocation-
	// free for the common already-normalised case.
	trimmed := strings.TrimRight(raw, "/")
	return trimmed + "/"
}
