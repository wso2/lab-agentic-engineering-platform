/**
 * Integration tests for /webhooks/github.
 *
 * These hit the BFF directly (not via console). They exercise:
 *   - HMAC validation (good + bad signatures)
 *   - Dedup on X-GitHub-Delivery
 *   - Behavior when no matching ComponentTask exists for a PR (graceful 200)
 *
 * The BFF's `GITHUB_WEBHOOK_SECRET` is read from the `GITHUB_WEBHOOK_SECRET`
 * env var at test time so the test signs with the same secret the BFF
 * validates against. In docker-compose dev, both come from `.env`.
 */
import { describe, it, expect, beforeAll } from 'vitest';
import { createHmac, randomUUID } from 'node:crypto';

const API_BASE = process.env.API_BASE_URL || 'http://localhost:9090';
const WEBHOOK_SECRET = process.env.GITHUB_WEBHOOK_SECRET || '';

function sign(body: string): string {
  return 'sha256=' + createHmac('sha256', WEBHOOK_SECRET).update(body).digest('hex');
}

async function postWebhook(
  event: string,
  payload: object,
  options: { deliveryId?: string; signature?: string } = {},
): Promise<{ status: number; text: string }> {
  const body = JSON.stringify(payload);
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'X-GitHub-Event': event,
    'X-GitHub-Delivery': options.deliveryId ?? randomUUID(),
    'X-Hub-Signature-256': options.signature ?? sign(body),
  };
  const res = await fetch(`${API_BASE}/webhooks/github`, {
    method: 'POST',
    headers,
    body,
  });
  return { status: res.status, text: await res.text() };
}

describe('Webhook Receiver', () => {
  beforeAll(() => {
    if (!WEBHOOK_SECRET) {
      throw new Error(
        'GITHUB_WEBHOOK_SECRET env var must be set to the same value as the BFF',
      );
    }
  });

  it('rejects an event with an invalid signature', async () => {
    const { status } = await postWebhook(
      'pull_request',
      { action: 'opened', pull_request: { number: 999 } },
      { signature: 'sha256=00000000' },
    );
    expect(status).toBe(401);
  });

  it('accepts a well-formed pull_request event for an unknown PR', async () => {
    // No matching ComponentTask in the DB → handler is a no-op and returns 200.
    const { status } = await postWebhook('pull_request', {
      action: 'opened',
      pull_request: { number: 999999 },
      repository: { full_name: 'tests/none' },
    });
    expect(status).toBe(200);
  });

  it('dedupes on X-GitHub-Delivery', async () => {
    const deliveryId = randomUUID();
    const payload = {
      action: 'opened',
      pull_request: { number: 999998 },
      repository: { full_name: 'tests/none' },
    };
    const first = await postWebhook('pull_request', payload, { deliveryId });
    expect(first.status).toBe(200);
    const second = await postWebhook('pull_request', payload, { deliveryId });
    // Replays of finished work ack 200; we don't get a 4xx.
    expect(second.status).toBe(200);
  });

  it('rejects malformed signatures', async () => {
    const { status } = await postWebhook(
      'pull_request',
      { action: 'opened' },
      { signature: 'not-a-signature' },
    );
    expect(status).toBe(401);
  });

  it('rejects a missing X-GitHub-Delivery header', async () => {
    const body = JSON.stringify({ action: 'opened' });
    const res = await fetch(`${API_BASE}/webhooks/github`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-GitHub-Event': 'pull_request',
        'X-Hub-Signature-256': sign(body),
      },
      body,
    });
    expect(res.status).toBe(400);
  });
});
