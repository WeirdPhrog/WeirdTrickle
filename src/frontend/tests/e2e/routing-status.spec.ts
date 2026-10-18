import { expect, test } from "@playwright/test";

test("suspended routing stays visible across tabs and clears after recovery without losing edits", async ({
  page,
}) => {
  let statuses = [
    {
      id: "abcdef12",
      name: "Group VPN",
      subscription: false,
      error: "kernel busy",
      nextRetryAt: 1800000060,
    },
    {
      id: "abcdef12",
      name: "ASN VPN",
      subscription: true,
      error: '<img src=x onerror="window.injected=true">',
      nextRetryAt: 1800000900,
    },
  ];
  let fail = false;
  await page.route("**/auth", (route) => route.fulfill({ json: { enabled: false } }));
  await page.route("**/interfaces", (route) =>
    route.fulfill({ json: { interfaces: [{ id: "wg0" }] } }),
  );
  await page.route("**/groups?with_rules=true", (route) =>
    route.fulfill({
      json: {
        groups: [
          {
            id: "abcdef12",
            name: "Group VPN",
            interface: "wg0",
            color: "#ffffff",
            enable: true,
            rules: [],
          },
        ],
      },
    }),
  );
  await page.route("**/subscriptions", (route) => route.fulfill({ json: { subscriptions: [] } }));
  await page.route("**/subscriptions/settings", (route) =>
    route.fulfill({ json: { fetchInterface: "" } }),
  );
  await page.route("**/system/routing", (route) =>
    fail
      ? route.fulfill({ status: 503, json: {} })
      : route.fulfill({ json: { suspended: statuses } }),
  );
  await page.goto("/");
  const warning = page.locator(".routing-warning");
  await expect(warning).toBeVisible();
  await expect(warning).toContainText("Group VPN");
  await expect(warning).toContainText("ASN VPN");
  await expect(warning).toContainText('<img src=x onerror="window.injected=true">');
  await expect(warning.locator("img")).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(warning).toBeVisible();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1))
    .toBe(true);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.locator("input.group-name").fill("Unsaved name");
  await expect(page.locator("#save-changes")).not.toHaveClass(/inactive/);
  await page.getByRole("tab", { name: /Subscriptions|Подписки/i }).click();
  await expect(warning).toBeVisible();

  fail = true;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(warning).toContainText(/last known|последнее известное/i);
  await expect(page.locator(".toast")).toHaveCount(0);
  fail = false;
  statuses = [];
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await expect(warning).toHaveCount(0);
  await page.getByRole("tab", { name: /Groups|Группы/i }).click();
  await expect(page.locator("input.group-name")).toHaveValue("Unsaved name");
  await expect(page.locator("#save-changes")).not.toHaveClass(/inactive/);
});

test("healthy routing keeps the panel free of recovery warnings", async ({ page }) => {
  await page.route("**/auth", (route) => route.fulfill({ json: { enabled: false } }));
  await page.route("**/interfaces", (route) => route.fulfill({ json: { interfaces: [] } }));
  await page.route("**/groups?with_rules=true", (route) => route.fulfill({ json: { groups: [] } }));
  await page.route("**/subscriptions", (route) => route.fulfill({ json: { subscriptions: [] } }));
  await page.route("**/subscriptions/settings", (route) =>
    route.fulfill({ json: { fetchInterface: "" } }),
  );
  await page.route("**/system/routing", (route) => route.fulfill({ json: { suspended: [] } }));
  const status = page.waitForResponse("**/system/routing");
  await page.goto("/");
  await status;
  await expect(page.getByRole("tab", { name: /Groups|Группы/i })).toBeVisible();
  await expect(page.locator(".routing-warning")).toHaveCount(0);
});
