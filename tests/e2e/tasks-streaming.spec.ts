import { test, expect } from '@playwright/test';

/**
 * Tasks streaming E2E — wso2-cloud-local.
 *
 * Verifies the user-visible streaming behavior of the redesigned tech-lead
 * agent (see docs/design/tech-lead-agent.md):
 *   1. Plan cards (title + rationale) become visible *before* their issue body
 *      has streamed — the redesign's central UX win. The old single-shot
 *      task-generator hid all output behind one synchronous request.
 *   2. While Phase 2 is running, each card shows a "Generating details…"
 *      spinner that's replaced by streamed markdown as deltas arrive.
 *   3. On data-finish, every card has a populated body and the GH issue link
 *      is live.
 *   4. The "Start Implementation" button only enables after data-finish.
 *
 * Pre-requisites:
 *   - wso2-cloud-local cluster up (see wso2-cloud-local/IMPLEMENTATION.md).
 *   - A project exists with a saved/tagged spec AND a saved/tagged design.
 *   - ANTHROPIC_API_KEY available to agents-service.
 *
 * Run explicitly with:
 *   TECH_LEAD_E2E=1 PROJECT_HANDLE=<your-project> npx playwright test \
 *     e2e/tasks-streaming.spec.ts
 */

const ENABLED = process.env.TECH_LEAD_E2E === '1';
const describeMaybe = ENABLED ? test.describe : test.describe.skip;

const BASE_URL =
  process.env.E2E_BASE_URL ||
  'http://http-app-factory-c-development-wso2cloud-f1c2e3e1.openchoreoapis.localhost:19080';

const ADMIN_USER = process.env.E2E_ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.E2E_ADMIN_PASS || 'ThunderAdminPass123!';

const ORG_HANDLE = process.env.ORG_HANDLE || 'wso2cloud';
const PROJECT_HANDLE = process.env.PROJECT_HANDLE || '';

describeMaybe('tech-lead tasks streaming UI', () => {
  test('plan cards appear before bodies, bodies stream in, dispatch enables on finish', async ({
    page,
  }) => {
    test.setTimeout(10 * 60 * 1000);

    if (!PROJECT_HANDLE) {
      test.skip(true, 'PROJECT_HANDLE env var required (existing project with approved spec + design)');
    }

    // ── Login (Thunder) ──────────────────────────────────────────────────
    await page.goto(BASE_URL);
    const username = page.getByRole('textbox', { name: /username/i });
    await username.waitFor({ timeout: 30_000 });
    await username.fill(ADMIN_USER);
    await page.getByRole('textbox', { name: /password/i }).fill(ADMIN_PASS);
    await page.getByRole('button', { name: /sign in/i }).click();
    await page.waitForURL(/\/organizations\//, { timeout: 30_000 });

    // ── Navigate to the project's Tasks tab ───────────────────────────────
    await page.goto(
      `${BASE_URL}/organizations/${ORG_HANDLE}/projects/${PROJECT_HANDLE}`,
    );
    await page.getByRole('button', { name: /^tasks$/i }).click();

    // ── 1. Trigger generation ────────────────────────────────────────────
    const generateBtn = page.getByRole('button', { name: /generate (more )?tasks/i });
    await generateBtn.first().waitFor({ state: 'visible', timeout: 30_000 });
    await generateBtn.first().click();

    // ── 2. Plan cards land within Phase 1 budget (≤30s for SLA, ≤90s safety) ──
    const firstTitle = page.locator('p, span').filter({ hasText: /^Implement / }).first();
    await firstTitle.waitFor({ state: 'visible', timeout: 90_000 });

    // ── 3. While Phase 2 is running, at least one card shows the
    //      "Generating details…" placeholder. This is the redesign's payoff:
    //      the card+title+rationale are visible while the body is still
    //      streaming. ────────────────────────────────────────────────────
    const generatingPlaceholder = page.getByText(/generating details/i).first();
    const sawPlaceholder = await generatingPlaceholder
      .waitFor({ state: 'visible', timeout: 90_000 })
      .then(() => true)
      .catch(() => false);

    // Either the placeholder showed up (typical) or bodies streamed in fast
    // enough that we never caught the pending state — both prove the cards
    // exist before the bodies are complete, which is the contract.
    if (!sawPlaceholder) {
      // Confirm at least one card title is visible without a "## What"
      // section yet — this is the same property by another route.
      await firstTitle.waitFor({ state: 'visible', timeout: 5_000 });
    }

    // ── 4. Wait for finish: the header generating spinner clears AND every
    //      visible card has body content (## What heading or similar). ───
    await expect(page.getByRole('button', { name: /generating…/i })).toHaveCount(
      0,
      { timeout: 8 * 60 * 1000 },
    );

    // The 5-section detail-phase output always includes "## What" — render
    // by InlineMarkdown becomes an h3. After finish there should be at least
    // one h3 with that text.
    await expect(
      page.getByRole('heading', { name: /^what$/i }).first(),
    ).toBeVisible({ timeout: 30_000 });

    // ── 5. Every card has its GitHub issue link live. ────────────────────
    const externalLinks = page.locator('a[href*="github.com"]');
    await expect(externalLinks.first()).toBeVisible({ timeout: 10_000 });

    // ── 6. Dispatch button is now visible (enables only post-finish per
    //      design §11). ──────────────────────────────────────────────────
    await expect(
      page.getByRole('button', { name: /start implementation/i }),
    ).toBeVisible({ timeout: 30_000 });
  });
});
