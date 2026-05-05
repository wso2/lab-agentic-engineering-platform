import { test, expect } from '@playwright/test';

/**
 * Spec wireframe generation — happy path E2E.
 *
 * Drives the console through:
 *   1. Create a project.
 *   2. Save a spec (auto-tags spec-v1).
 *   3. Click "Generate Wireframe" — kicks off async generation.
 *   4. Poll the modal until the iframe renders the HTML wireframe.
 *   5. Assert the rendered HTML uses MVP.css + Alpine.js (the wireframe
 *      agent's contract).
 *
 * Skipped by default — depends on a fully bootstrapped local stack
 * (docker compose up + agents-service on :3400 + ANTHROPIC_API_KEY in
 * deployments/.env). Run with `npx playwright test --grep wireframe`.
 */
test.describe.skip('Spec Wireframe Generation', () => {
  test('generates wireframe and renders it in the iframe', async ({ page }) => {
    await page.goto('/');

    // Create a project.
    await page.getByRole('button', { name: /new project/i }).click();
    const projectName = `wireframe-e2e-${Date.now()}`;
    await page.getByLabel(/project name/i).fill(projectName);
    await page.getByRole('button', { name: /create/i }).click();

    await expect(page).toHaveURL(/\/organizations\/.+\/projects\/.+/);

    // Spec → fill in a small markdown spec, save & approve.
    await page.getByRole('tab', { name: /spec/i }).click();
    await page
      .getByRole('textbox', { name: /spec/i })
      .fill(
        '# TODO App\n\nUsers can sign in, add todos, mark them complete, and view a history page.\n',
      );
    await page.getByRole('button', { name: /save & proceed/i }).click();

    // Open the wireframe modal and trigger generation.
    await page.getByRole('button', { name: /generate wireframe/i }).click();

    // The agent generation typically takes 30-90s. The modal polls the
    // status endpoint every 3s — wait for the rendered iframe.
    const iframe = page.locator('iframe[sandbox]');
    await expect(iframe).toBeVisible({ timeout: 3 * 60 * 1000 });

    // The iframe content is provided via srcDoc (no network fetch). Read
    // it via the DOM attribute and validate the wireframe contract.
    const srcDoc = await iframe.getAttribute('srcdoc');
    expect(srcDoc).toBeTruthy();
    expect(srcDoc!.toLowerCase()).toContain('<!doctype');
    expect(srcDoc).toContain('mvp.css');
    expect(srcDoc).toContain('alpinejs');
  });
});
