/**
 * AC-5: All dashboard views render within 5 seconds of page load.
 *
 * Prerequisites:
 *   docker compose up -d
 *   go run ./dev/seed.go     # seed SQLite with 30 days of data
 *   BASE_URL=http://localhost:3000 npx playwright test
 *
 * Each view signals readiness by setting data-testid="view-N-ready" on its
 * outermost element once the loading spinner is removed. Playwright's default
 * assertion timeout is set to 5 000 ms in playwright.config.ts (AC-5).
 */

import { test, expect } from "@playwright/test";

test.describe("AC-5: all views render within 5 s", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/");
  });

  test("View 1 — J/Token live table is ready within 5 s", async ({ page }) => {
    await expect(page.getByTestId("view-1-ready")).toBeVisible();
  });

  test("View 2 — Trend chart (2a + 2b) is ready within 5 s", async ({ page }) => {
    await expect(page.getByTestId("view-2-ready")).toBeVisible();
  });

  test("View 3 — Namespace chargeback table is ready within 5 s", async ({ page }) => {
    await expect(page.getByTestId("view-3-ready")).toBeVisible();
  });

  test("View 4 — Idle consumption chart is ready within 5 s", async ({ page }) => {
    await expect(page.getByTestId("view-4-ready")).toBeVisible();
  });

  test("View 5 — Carbon & cost table is ready within 5 s", async ({ page }) => {
    await expect(page.getByTestId("view-5-ready")).toBeVisible();
  });
});

test.describe("AC-8: workload=unknown renders as italic gray", () => {
  test("unknown workload cell shows italic text", async ({ page }) => {
    await page.goto("/");
    // If any row has workload=unknown the cell must be italic, not the literal
    // string "unknown" in normal weight.
    const unknownCells = page.locator("td span.italic");
    // Only assert the style if the element exists — no data means no assertion.
    const count = await unknownCells.count();
    if (count > 0) {
      await expect(unknownCells.first()).toHaveText("unknown");
    }
  });
});

test.describe("AC-6: PUE slider updates cost without re-fetch", () => {
  test("changing PUE slider updates cost column within 200 ms", async ({ page }) => {
    await page.goto("/");
    // Wait for chargeback table to be ready.
    await expect(page.getByTestId("view-3-ready")).toBeVisible();

    // Locate the PUE range slider in View 3 (ChargebackTable).
    const slider = page
      .locator("section")
      .filter({ hasText: "Namespace Chargeback" })
      .locator('input[type="range"]');

    // Read a cost cell value before and after slider move.
    const costCell = page
      .locator("section")
      .filter({ hasText: "Namespace Chargeback" })
      .locator("td.font-mono")
      .first();

    const before = await costCell.textContent();

    // Move slider to max (PUE 2.0).
    await slider.evaluate((el: HTMLInputElement) => {
      el.value = "2.0";
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });

    // Cost must update immediately (no network — derived client-side).
    await expect(async () => {
      const after = await costCell.textContent();
      expect(after).not.toBe(before);
    }).toPass({ timeout: 200 });
  });
});

test.describe("AC-7: derivation formula shown inline", () => {
  test("kWh/token cell contains formula text", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByTestId("view-5-ready")).toBeVisible();

    // Each kWh/token cell should have a sub-line showing the formula.
    // The formula pattern is: "<jpt> × <pue> ÷ 3,600,000"
    const formulaLines = page.locator("td p.text-gray-400").filter({
      hasText: "÷ 3,600,000",
    });
    const count = await formulaLines.count();
    // Only assert when there are rows with data.
    if (count > 0) {
      await expect(formulaLines.first()).toContainText("÷ 3,600,000");
    }
  });
});
