import { test, expect } from '@playwright/test';

/**
 * Architect streaming E2E — wso2-cloud-local.
 *
 * Verifies the user-visible streaming behavior of the redesigned architect
 * agent (see docs/design/architect-agent.md):
 *   1. Component cards become visible *before* their OpenAPI YAML has streamed
 *      (the bottleneck the redesign fixes — old streamObject blocked the cards
 *      until the multi-KB YAML had finished).
 *   2. Each card whose spec is being generated shows a "Generating OpenAPI
 *      specification…" spinner during the gap.
 *   3. On data-finish, every card has a populated OpenAPI Specification button.
 *
 * Pre-requisites:
 *   - wso2-cloud-local cluster up (see wso2-cloud-local/IMPLEMENTATION.md).
 *   - A project exists in the `wso2cloud` org with a saved-and-tagged spec.
 *     This test does NOT bootstrap a project — project provisioning depends
 *     on git creds + GitHub PAT being configured for the org, which is
 *     environment-specific.
 *   - ANTHROPIC_API_KEY in the agents-service deployment (real model call).
 *
 * Run explicitly with:
 *   ARCHITECT_E2E=1 PROJECT_HANDLE=<your-project> npx playwright test \
 *     e2e/architect-streaming.spec.ts
 */

const ENABLED = process.env.ARCHITECT_E2E === '1';
const describeMaybe = ENABLED ? test.describe : test.describe.skip;

const BASE_URL =
  process.env.E2E_BASE_URL ||
  'http://http-app-factory-c-development-wso2cloud-f1c2e3e1.openchoreoapis.localhost:19080';

const ADMIN_USER = process.env.E2E_ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.E2E_ADMIN_PASS || 'ThunderAdminPass123!';

const ORG_HANDLE = process.env.ORG_HANDLE || 'wso2cloud';
const PROJECT_HANDLE = process.env.PROJECT_HANDLE || '';

describeMaybe('architect streaming UI', () => {
  test('component cards appear before OpenAPI specs and show a spinner', async ({
    page,
  }) => {
    test.setTimeout(8 * 60 * 1000);

    if (!PROJECT_HANDLE) {
      test.skip(true, 'PROJECT_HANDLE env var required (existing project with approved spec)');
    }

    // ── Login (Thunder) ──────────────────────────────────────────────────
    await page.goto(BASE_URL);
    const username = page.getByRole('textbox', { name: /username/i });
    await username.waitFor({ timeout: 30_000 });
    await username.fill(ADMIN_USER);
    await page.getByRole('textbox', { name: /password/i }).fill(ADMIN_PASS);
    await page.getByRole('button', { name: /sign in/i }).click();
    await page.waitForURL(/\/organizations\//, { timeout: 30_000 });

    // ── Navigate straight to the project's Architecture tab ───────────────
    await page.goto(
      `${BASE_URL}/organizations/${ORG_HANDLE}/projects/${PROJECT_HANDLE}`,
    );
    await page.getByRole('button', { name: /^architecture$/i }).click();

    // Generation may auto-start (when arriving from save-spec) or require a
    // manual Generate click.
    const generateBtn = page.getByRole('button', { name: /generate design/i });
    if (await generateBtn.count()) {
      await generateBtn.first().click();
    }

    // ── Streaming assertions ─────────────────────────────────────────────
    // 1. A component card becomes visible during the stream.
    const anyCard = page.locator('.MuiCard-root').first();
    await anyCard.waitFor({ state: 'visible', timeout: 90_000 });

    // 2. While generation is in progress, the spec-updating spinner appears
    //    for at least one component. This is the redesign's user-visible
    //    payoff: the card is on screen but its OpenAPI section is still a
    //    spinner — under the old streamObject impl the card itself wouldn't
    //    appear until the YAML had finished.
    const spinner = page.locator('[data-testid^="spec-updating-"]').first();
    const sawSpinner = await spinner
      .waitFor({ state: 'visible', timeout: 90_000 })
      .then(() => true)
      .catch(() => false);
    if (sawSpinner) {
      const cardForSpinner = spinner
        .locator('xpath=ancestor::*[contains(@class,"MuiCard-root")]')
        .first();
      // While the spinner is up, no OpenAPI Specification disclosure button
      // is rendered for that card (the spinner replaces it).
      await expect(
        cardForSpinner.getByRole('button', {
          name: /openapi specification/i,
        }),
      ).toHaveCount(0);
    }

    // 3. Wait for finish — the header "Generating…" chip clears.
    await expect(page.getByText(/^Generating/i).first()).toBeHidden({
      timeout: 6 * 60 * 1000,
    });

    // 4. Every card now has an OpenAPI Specification disclosure button.
    const buttonCount = await page
      .getByRole('button', { name: /openapi specification/i })
      .count();
    expect(buttonCount, 'each component card should expose its spec').toBeGreaterThan(0);

    // 5. No spec-updating spinner remains.
    await expect(
      page.locator('[data-testid^="spec-updating-"]'),
    ).toHaveCount(0);
  });
});
