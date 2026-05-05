import { describe, it, expect } from 'vitest';
import { apiGet, apiPost, apiDelete } from '../helpers/api-client';

describe('Projects API', () => {
  it('should list projects', async () => {
    const { status, data } = await apiGet<{ items: any[] }>('/api/v1/projects');
    expect(status).toBe(200);
    expect(data).toHaveProperty('items');
    expect(Array.isArray(data.items)).toBe(true);
  });

  it('should create a project', async () => {
    const name = `test-proj-${Date.now()}`;
    const { status, data } = await apiPost<any>('/api/v1/projects', {
      name,
      displayName: 'Test Project',
      description: 'Created by integration test',
    });
    expect(status).toBe(201);
    expect(data.name).toBe(name);

    // Cleanup
    await apiDelete(`/api/v1/projects/${name}`);
  });

  it('should get a project by name', async () => {
    const name = `test-proj-get-${Date.now()}`;
    await apiPost('/api/v1/projects', { name, displayName: 'Get Test' });

    const { status, data } = await apiGet<any>(`/api/v1/projects/${name}`);
    expect(status).toBe(200);
    expect(data.name).toBe(name);

    await apiDelete(`/api/v1/projects/${name}`);
  });

  it('should delete a project', async () => {
    const name = `test-proj-del-${Date.now()}`;
    await apiPost('/api/v1/projects', { name, displayName: 'Delete Test' });

    const { status } = await apiDelete(`/api/v1/projects/${name}`);
    expect(status).toBe(204);
  });

  it('should reject create without name', async () => {
    const { status } = await apiPost<any>('/api/v1/projects', {
      displayName: 'No Name',
    });
    expect(status).toBe(400);
  });
});
