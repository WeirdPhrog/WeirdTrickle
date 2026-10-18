import { expect, test } from "@playwright/test";

test.describe("Header Settings", () => {
  test.beforeEach(async ({ page }) => {
    // Mock auth disabled to access layout directly
    await page.route("**/auth", async (route) => route.fulfill({ json: { enabled: false } }));
    await page.route("**/groups?with_rules=true", async (route) =>
      route.fulfill({ json: { groups: [] } }),
    );
    await page.route("**/interfaces", async (route) => route.fulfill({ json: { interfaces: [] } }));
    await page.goto("/");
  });

  test("should display version", async ({ page }) => {
    // Version is in .version span.version-text
    const version = page.locator(".version .version-text");
    await expect(version).toBeVisible();
    expect(version.textContent.length).toBeGreaterThan(0);
  });

  test("should rotate locale", async ({ page }) => {
    const localeBtn = page.locator(".locale button");

    // Get initial text (flag)
    const initialText = await localeBtn.textContent();

    // Click to rotate
    await localeBtn.click();

    // Verify text changed
    await expect(localeBtn).not.toHaveText(initialText || "");

    // Click again to rotate back (assuming 2 locales, or just rotate more)
    await localeBtn.click();
  });

  test("should open info dialog", async ({ page }) => {
    const infoBtn = page.locator(".info button");
    await expect(infoBtn).toBeVisible();

    await infoBtn.click();

    // Check dialog title
    const dialog = page.locator("[data-dialog-content]");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText("About", { exact: true })).toBeVisible();

    const issueLink = dialog.locator('a[href="https://github.com/WeirdPhrog/WeirdTrickle/issues"]');
    await expect(issueLink).toBeVisible();
    await expect(issueLink.locator("..")).toContainText("Report a WeirdTrickle issue");
    await expect(issueLink).toHaveAttribute("rel", "noopener noreferrer");
    await expect(
      dialog.getByRole("heading", { name: "Original MagiTrickle project" }),
    ).toBeVisible();
    await expect(dialog.getByText("Official website")).toBeVisible();
    await expect(dialog.getByText("MagiTrickle Bug Tracker")).toBeVisible();

    // The taller About dialog must stay usable on a phone in landscape.
    await page.setViewportSize({ width: 844, height: 390 });
    await expect(dialog.getByText("About", { exact: true })).toBeInViewport();
    await expect(dialog.locator("[data-dialog-close]")).toBeInViewport();
    const build = dialog.locator(".build");
    await build.scrollIntoViewIfNeeded();
    await expect(build).toBeInViewport();

    // Close dialog
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  });
});
