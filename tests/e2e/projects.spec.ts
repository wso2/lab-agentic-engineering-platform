import { test, expect } from '@playwright/test';

test.describe('Projects', () => {
  test('should show the org overview page', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h4')).toContainText('Projects');
  });

  test('should navigate to create project page', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: /new project/i }).click();
    await expect(page.locator('h4')).toContainText('Create Project');
  });
});
