/**
 * Test API client — direct HTTP to the Go backend.
 */

const API_BASE = process.env.API_BASE_URL || 'http://localhost:8080';

export async function apiGet<T>(path: string): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`);
  const data = res.status === 204 ? (undefined as T) : await res.json();
  return { status: res.status, data };
}

export async function apiPost<T>(path: string, body: unknown): Promise<{ status: number; data: T }> {
  const res = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = res.status === 204 ? (undefined as T) : await res.json();
  return { status: res.status, data };
}

export async function apiDelete(path: string): Promise<{ status: number }> {
  const res = await fetch(`${API_BASE}${path}`, { method: 'DELETE' });
  return { status: res.status };
}
