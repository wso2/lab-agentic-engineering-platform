import { describe, it, expect } from 'vitest';
import { apiGet } from '../helpers/api-client';

describe('Health Check', () => {
  it('should return ok', async () => {
    const { status, data } = await apiGet<{ status: string }>('/health');
    expect(status).toBe(200);
    expect(data.status).toBe('ok');
  });
});
