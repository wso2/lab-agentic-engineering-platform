// Mirror of asdlc-service/services/external_url.go. The AP router 404s
// on bare context paths without a trailing slash — see gotcha C3 in
// deployments/POC-API-PLATFORM.md. ALL console code that renders a
// component external URL MUST pass it through this helper.
export function normalizeExternalUrl(raw: string | null | undefined): string {
  if (!raw) return "";
  return raw.replace(/\/+$/g, "") + "/";
}
