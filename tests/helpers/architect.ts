/**
 * Helpers for calling the agents-service /v1/agents/architect endpoint
 * directly from tests. Mints a service JWT against Thunder using the same
 * client_credentials app the BFF uses, then forwards it as Bearer auth.
 *
 * Pre-req for the test runner: kubectl port-forward
 *   svc/app-factory-agents-service 13400:3400
 *   -n dp-wso2cloud-app-factory-development-bad5f211
 * (or override AGENTS_SERVICE_URL).
 */

const AGENTS_URL =
  process.env.AGENTS_SERVICE_URL || 'http://localhost:13400';
const TOKEN_URL =
  process.env.THUNDER_TOKEN_URL ||
  'http://platform-idp.127.0.0.1.nip.io:19080/oauth2/token';
const CLIENT_ID =
  process.env.AGENTS_CLIENT_ID || 'asdlc-bff-to-agents-service';
const CLIENT_SECRET =
  process.env.AGENTS_CLIENT_SECRET || 'asdlc-bff-to-agents-service-secret';

let cachedToken: { token: string; expiresAt: number } | null = null;

export async function getServiceToken(): Promise<string> {
  if (cachedToken && cachedToken.expiresAt > Date.now() + 60_000) {
    return cachedToken.token;
  }
  const body = new URLSearchParams({
    grant_type: 'client_credentials',
    client_id: CLIENT_ID,
    client_secret: CLIENT_SECRET,
  });
  const res = await fetch(TOKEN_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body,
  });
  if (!res.ok) {
    throw new Error(
      `Thunder token request failed: ${res.status} ${await res.text()}`,
    );
  }
  const json = (await res.json()) as {
    access_token: string;
    expires_in: number;
  };
  cachedToken = {
    token: json.access_token,
    expiresAt: Date.now() + json.expires_in * 1000,
  };
  return json.access_token;
}

export type ArchitectInput = {
  projectName: string;
  spec: string;
  previousDesign?: unknown;
};

export async function callArchitect(input: ArchitectInput): Promise<Response> {
  const token = await getServiceToken();
  return fetch(`${AGENTS_URL}/v1/agents/architect`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'text/event-stream',
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(input),
  });
}
