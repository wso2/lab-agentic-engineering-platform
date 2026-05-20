/**
 * Reset test state between test suites.
 * Calls the _test/reset endpoint (only available when TEST_MODE=true).
 */

const API_BASE = process.env.API_BASE_URL || 'http://localhost:8080';

export async function resetTestState(): Promise<void> {
  await fetch(`${API_BASE}/api/v1/_test/reset`, { method: 'POST' });
}
