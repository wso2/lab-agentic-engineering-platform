import { describe, it, expect } from 'vitest';
import { apiGet } from '../helpers/api-client';

describe('Components API', () => {
  it('should list components for a project', async () => {
    const { status, data } = await apiGet<{ items: any[] }>('/api/v1/projects/some-project/components');
    expect(status).toBe(200);
    expect(data).toHaveProperty('items');
    expect(Array.isArray(data.items)).toBe(true);
  });
});
