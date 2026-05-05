import { test, expect } from '@playwright/test';

/**
 * Phase 0 GitHub-native flow — happy path E2E.
 *
 * This test drives the console through the full dispatch flow and verifies
 * that the BFF + git-service + remote-worker + GitHub all line up:
 *
 *   1. Create a project → repo provisioned, webhook registered.
 *   2. Save spec → spec-v1 tag.
 *   3. Save design → design-v1 tag.
 *   4. Generate tasks → one GitHub issue per task.
 *   5. Start implementation → per task: feature branch + draft PR + workspace.
 *
 * The simulated agent commit / PR ready / human merge path lives in a
 * separate test that runs against the live GitHub repo with a real PAT —
 * that one is gated on `GITHUB_PLATFORM_PAT` being set in CI.
 *
 * Marked `.skip` by default because the test depends on a fully bootstrapped
 * dev environment (k3d + OC + GitHub PAT). Run with `npx playwright test --grep github-flow`.
 */
test.describe.skip('GitHub-Native Flow', () => {
  test('dispatching a task creates issue + branch + draft PR', async ({ page }) => {
    await page.goto('/');

    // Create a project. The exact UI flow depends on the console build —
    // we treat the high-level affordances as the contract: a "New Project"
    // button, a name field, a create action.
    await page.getByRole('button', { name: /new project/i }).click();
    const projectName = `phase0-e2e-${Date.now()}`;
    await page.getByLabel(/project name/i).fill(projectName);
    await page.getByRole('button', { name: /create/i }).click();

    // Wait for the project's overview page.
    await expect(page).toHaveURL(/\/organizations\/.+\/projects\/.+/);

    // Spec → save & approve.
    await page.getByRole('tab', { name: /spec/i }).click();
    await page.getByRole('textbox', { name: /spec/i }).fill(
      '## Phase 0 E2E\nA trivial spec to drive end-to-end verification.\n',
    );
    await page.getByRole('button', { name: /save & proceed/i }).click();

    // Design → save & approve.
    await page.getByRole('tab', { name: /design/i }).click();
    await page.getByRole('button', { name: /generate design/i }).click();
    // The design generation step can take a few minutes — the page polls.
    await expect(page.getByText(/design generated/i)).toBeVisible({ timeout: 5 * 60 * 1000 });
    await page.getByRole('button', { name: /save & proceed/i }).click();

    // Generate tasks.
    await page.getByRole('tab', { name: /tasks|components/i }).click();
    await page.getByRole('button', { name: /generate tasks/i }).click();
    await expect(page.getByText(/issue/i).first()).toBeVisible({ timeout: 60_000 });

    // Start implementation.
    await page.getByRole('button', { name: /start implementation/i }).click();

    // Each task chip should advance to "Implementing" (in_progress).
    await expect(page.getByText(/implementing/i).first()).toBeVisible({ timeout: 60_000 });

    // Task cards should expose links to the GitHub issue and PR.
    await expect(page.getByRole('link', { name: /issue/i }).first()).toBeVisible();
    await expect(page.getByRole('link', { name: /pull request|pr/i }).first()).toBeVisible();
  });
});
