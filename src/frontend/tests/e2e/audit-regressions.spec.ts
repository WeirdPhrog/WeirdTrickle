import { expect, test, type Page, type Route } from "@playwright/test";

import { GroupsPage } from "./pages/GroupsPage";

const rule = (index: number) => ({
  id: index.toString(16).padStart(8, "0"),
  name: `Rule ${index}`,
  rule: `rule${index}.example.com`,
  type: "domain",
  enable: true,
});
const group = (rules = [rule(1)]) => ({
  id: "abcdef12",
  name: "Test Group",
  rules,
  color: "#000000",
  enable: true,
  interface: "eth0",
});

async function setup(page: Page, groups = [group()], subscriptions: unknown[] = []) {
  await page.route("**/auth", (route) => route.fulfill({ json: { enabled: false } }));
  await page.route("**/groups?with_rules=true", (route) => route.fulfill({ json: { groups } }));
  await page.route("**/interfaces", (route) =>
    route.fulfill({ json: { interfaces: [{ id: "eth0" }] } }),
  );
  await page.route("**/subscriptions/settings", (route) =>
    route.fulfill({ json: { fetchInterface: "" } }),
  );
  await page.route("**/subscriptions", (route) => route.fulfill({ json: { subscriptions } }));
  await page.goto("/");
  await expect(page.locator(".group-controls")).toBeVisible();
}

test("same rule IDs in different groups do not hide unsaved changes", async ({ page }) => {
  await setup(page, [
    group([{ ...rule(1), enable: true }]),
    { ...group([{ ...rule(1), enable: false }]), id: "abcdef13" },
  ]);
  await page.locator(".group [data-collapsible-trigger]").first().click();
  const toggle = page.locator(".group").first().locator(".rule [role=switch]");
  await toggle.click();
  await expect(page.locator("#save-changes")).not.toHaveClass(/inactive/);
  await expect
    .poll(() =>
      page.evaluate(() => !window.dispatchEvent(new Event("beforeunload", { cancelable: true }))),
    )
    .toBe(true);
  await toggle.click();
  await expect(page.locator("#save-changes")).toHaveClass(/inactive/);
});

test("invalid rules outside the current page block saving and retain the unload warning", async ({
  page,
}) => {
  const rules = Array.from({ length: 60 }, (_, index) => rule(index + 1));
  rules[55] = { ...rules[55], type: "subnet6", rule: "1::2::3" };
  await setup(page, [group(rules)]);
  const groups = new GroupsPage(page);
  await groups.setGroupName(0, "Edited group");
  await expect(page.locator('.rule[data-index="55"]')).toHaveCount(0);
  await expect(groups.saveButton).toHaveClass(/inactive/);
  await groups.search("rule1.example.com");
  await expect(page.locator(".rule")).toHaveCount(1);
  await expect(groups.saveButton).toHaveClass(/inactive/);
  let saves = 0;
  await page.route("**/groups?save=true", (route) => {
    saves++;
    return route.fulfill({ json: {} });
  });
  await page.keyboard.press("Control+s");
  await expect
    .poll(() =>
      page.evaluate(() => !window.dispatchEvent(new Event("beforeunload", { cancelable: true }))),
    )
    .toBe(true);
  expect(saves).toBe(0);
});

test("invalid subscription rules can be disabled without losing the remaining edits", async ({
  page,
}) => {
  await setup(
    page,
    [],
    [
      {
        id: "11223344",
        name: "List",
        interface: "eth0",
        enable: true,
        url: "https://one.example/list",
        lastUpdate: 1700000000,
        interval: 86400,
        rules: [{ ...rule(1), type: "subnet6", rule: "1::2::3" }],
      },
    ],
  );
  await page.getByRole("tab", { name: "Subscriptions" }).click();
  await page.locator(".subscription-url-input").fill("https://two.example/list");
  await expect(page.locator("#save-subscriptions")).toHaveClass(/inactive/);
  await page.locator(".subscription-panel .action.collapse button").click();
  await expect(page.locator(".subscription-rule .invalid")).toBeVisible();
  await page.locator(".subscription-rule [role=switch]").click();
  await expect(page.locator("#save-subscriptions")).not.toHaveClass(/inactive/);
});

test("closing a subscription preview cancels stale results before reopening", async ({ page }) => {
  await setup(page, []);
  await page.getByRole("tab", { name: "Subscriptions" }).click();
  let oldRequest: Route | undefined;
  await page.route("**/subscriptions/rules?url=**", (route) => {
    if (route.request().url().includes("old.example")) {
      oldRequest = route;
      return;
    }
    return route.fulfill({ json: { rules: [rule(1), rule(2)] } });
  });
  const add = page.locator('[data-value="Add Subscription"] button');
  await add.click();
  const dialog = page.locator("[data-dialog-content]");
  await dialog.locator("#sub-url").fill("https://old.example/list");
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  await expect.poll(() => Boolean(oldRequest)).toBe(true);
  await expect(dialog.locator("#sub-url")).toBeDisabled();
  await dialog.locator(".close").click();
  await add.click();
  await dialog.locator("#sub-url").fill("https://new.example/list");
  await oldRequest!.fulfill({ json: { rules: [rule(99)] } }).catch(() => {});
  await expect(dialog.locator("#sub-url")).toHaveValue("https://new.example/list");
  await dialog.getByRole("button", { name: "Next", exact: true }).click();
  await expect(dialog.locator(".rules-count-row .total")).toHaveText("Found rules: 2");
  await expect(dialog.locator(".helper-text")).not.toHaveClass(/visible/);
});

test("import immediately after a type change uses the current type", async ({ page }) => {
  await setup(page, [group([])]);
  await page.locator('[data-value="Import Rule List"] button').click();
  const dialog = page.locator("[data-dialog-content]");
  await dialog.locator("textarea").fill("example.com");
  await dialog.locator("textarea").blur();
  await expect(dialog.locator(".results-view .badge")).toHaveText("namespace");
  await dialog.locator("[data-select-trigger]").click();
  await page.getByRole("option", { name: "Wildcard", exact: true }).click();
  await page.keyboard.press("Control+Enter");
  await expect(dialog).toBeHidden();
  await expect(page.locator(".rule .type")).toContainText("Wildcard");
});

test("closing an import during parsing leaves the next dialog empty", async ({ page }) => {
  await setup(page, [group([])]);
  const importButton = page.locator('[data-value="Import Rule List"] button');
  await importButton.click();
  const dialog = page.locator("[data-dialog-content]");
  await dialog.locator("textarea").fill("old.example.com");
  await dialog.locator("textarea").blur();
  await dialog.locator(".close").click();
  await importButton.click();
  await expect(dialog.locator("textarea")).toBeEnabled();
  await expect(dialog.locator("textarea")).toHaveValue("");
  await dialog.locator("textarea").fill("new.example.com");
  await page.keyboard.press("Control+Enter");
  await expect(dialog).toBeHidden();
  await expect(page.locator(".rule .pattern-input")).toHaveValue("new.example.com");
});

for (const storageMode of ["corrupt", "blocked"] as const) {
  test(`WebUI starts with ${storageMode} browser storage`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.addInitScript((mode) => {
      if (mode === "corrupt") {
        localStorage.setItem("auth", "null");
        localStorage.setItem("locale", '{"value":"missing-locale"}');
      } else {
        Storage.prototype.getItem = () => {
          throw new DOMException("Blocked", "SecurityError");
        };
        Storage.prototype.setItem = () => {
          throw new DOMException("Full", "QuotaExceededError");
        };
      }
    }, storageMode);
    await setup(page);
    await expect(page.locator(".group-name")).toHaveValue("Test Group");
    expect(errors).toEqual([]);
  });
}

test("saved authentication survives reloading the page", async ({ page }) => {
  await setup(page);
  await page.evaluate(() => localStorage.setItem("auth", '{"value":"saved-token"}'));
  await page.route("**/auth", (route) => route.fulfill({ json: { enabled: true } }));
  let authorization: string | undefined;
  await page.route("**/groups?with_rules=true", (route) => {
    authorization = route.request().headers().authorization;
    return route.fulfill({ json: { groups: [group()] } });
  });
  await page.reload();
  await expect(page.locator(".group-controls")).toBeVisible();
  expect(authorization).toBe("Bearer saved-token");
});
