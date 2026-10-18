import { expect, test, type Page } from "@playwright/test";

type Subscription = {
  id: string;
  name: string;
  interface: string;
  enable: boolean;
  url: string;
  lastUpdate: number;
  interval: number;
  rules: { enable: boolean; id: string; rule: string; type: string }[];
};

const makeSubscription = (id: string, url: string, name = `Subscription ${id}`): Subscription => ({
  id,
  name,
  interface: "eth0",
  enable: true,
  url,
  lastUpdate: 1700000000,
  interval: 86400,
  rules: [{ enable: true, id: `${id.slice(0, 4)}aaaa`, rule: `${id}.example.com`, type: "domain" }],
});

async function setupSubscriptions(page: Page, data: Subscription[]) {
  let subscriptions = structuredClone(data);
  let savedBody: { subscriptions: Subscription[] } | null = null;
  let syncBody: { url?: string } | null = null;
  let fetchInterface = "";
  let failSettingsSave = false;

  await page.route("**/auth", async (route) => route.fulfill({ json: { enabled: false } }));
  await page.route("**/groups?with_rules=true", async (route) =>
    route.fulfill({ json: { groups: [] } }),
  );
  await page.route("**/interfaces", async (route) =>
    route.fulfill({
      json: { interfaces: [{ id: "blackhole" }, { id: "eth0" }, { id: "wg0", name: "My VPN" }] },
    }),
  );
  await page.route("**/subscriptions/settings", async (route) => {
    if (route.request().method() === "PUT") {
      if (failSettingsSave) {
        await route.fulfill({ status: 500, json: { error: "disk full" } });
        return;
      }
      fetchInterface = route.request().postDataJSON().fetchInterface;
    }
    await route.fulfill({ json: { fetchInterface } });
  });
  await page.route("**/subscriptions", async (route) => {
    if (route.request().method() === "PUT") {
      savedBody = route.request().postDataJSON();
      subscriptions = structuredClone(savedBody?.subscriptions ?? []);
      await route.fulfill({ json: { status: "ok" } });
      return;
    }

    await route.fulfill({ json: { subscriptions } });
  });
  await page.route("**/subscriptions/*/sync", async (route) => {
    syncBody = route.request().postDataJSON();
    const id = route.request().url().split("/").at(-2);
    const subscription = subscriptions.find((item) => item.id === id);
    if (!subscription) {
      await route.fulfill({ status: 404, json: { error: "subscription not found" } });
      return;
    }

    subscription.url = syncBody?.url ?? subscription.url;
    subscription.rules = [
      { enable: true, id: "87654321", rule: "synced.example.com", type: "domain" },
    ];
    subscription.lastUpdate = 1800000000;

    await route.fulfill({
      json: {
        rules: subscription.rules,
        lastUpdate: subscription.lastUpdate,
        url: subscription.url,
      },
    });
  });

  await page.goto("/");
  await page.getByRole("tab", { name: "Subscriptions" }).click();
  await expect(page.locator(".subscription-panel")).toHaveCount(data.length);

  return {
    get savedBody() {
      return savedBody;
    },
    get syncBody() {
      return syncBody;
    },
    get fetchInterface() {
      return fetchInterface;
    },
    set failSettingsSave(value: boolean) {
      failSettingsSave = value;
    },
  };
}

test.describe("Subscriptions", () => {
  test("saves one shared download interface immediately, restores it and resets to default", async ({
    page,
  }) => {
    const state = await setupSubscriptions(page, [
      makeSubscription("a1b2c3d4", "https://one.example/list.txt"),
      makeSubscription("b1c2d3e4", "https://two.example/list.txt"),
    ]);
    const select = page.getByRole("button", { name: "Download interface", exact: true });
    await expect(select).toHaveCount(1);
    await expect(select).toContainText("System default");
    await select.click();
    await expect(page.getByRole("option", { name: "blackhole", exact: true })).toHaveCount(0);
    await page.getByRole("option", { name: "wg0 My VPN" }).click();
    await expect.poll(() => state.fetchInterface).toBe("wg0");
    await expect(select).toBeEnabled();
    await expect(page.locator("#save-subscriptions")).toHaveClass(/inactive/);
    await expect(page.locator(".subscription-interface")).toHaveText(["eth0", "eth0"]);

    await page.reload();
    await page.getByRole("tab", { name: "Subscriptions" }).click();
    await expect(select).toContainText("wg0");
    await select.click();
    await page.getByRole("option", { name: "System default", exact: true }).click();
    await expect.poll(() => state.fetchInterface).toBe("");
    await expect(select).toContainText("System default");
  });

  test("rolls back the selector when saving fails, including on an empty page", async ({
    page,
  }) => {
    const state = await setupSubscriptions(page, []);
    state.failSettingsSave = true;
    const select = page.getByRole("button", { name: "Download interface", exact: true });
    await expect(select).toBeEnabled();
    await select.click();
    await page.getByRole("option", { name: "wg0 My VPN" }).click();
    await expect(page.getByText("Request failed: disk full")).toBeVisible();
    await expect(select).toContainText("System default");
    expect(state.fetchInterface).toBe("");
  });

  test("retains an unavailable interface and offers retry when settings cannot load", async ({
    page,
  }) => {
    await setupSubscriptions(page, []);
    let failLoad = true;
    await page.route("**/subscriptions/settings", (route) =>
      failLoad
        ? route.fulfill({ status: 503, json: { error: "unavailable" } })
        : route.fulfill({ json: { fetchInterface: "offline-vpn" } }),
    );
    await page.reload();
    await page.getByRole("tab", { name: "Subscriptions" }).click();
    const select = page.getByRole("button", { name: "Download interface", exact: true });
    await expect(select).toBeDisabled();
    failLoad = false;
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(select).toContainText("offline-vpn");
    await expect(select).toBeEnabled();
  });

  test("edits an existing subscription URL and saves it", async ({ page }) => {
    const state = await setupSubscriptions(page, [
      makeSubscription("a1b2c3d4", "https://old.example/list.txt"),
    ]);

    const urlInput = page.locator(".subscription-url-input").first();
    await expect(urlInput).toHaveValue("https://old.example/list.txt");

    await urlInput.fill(" https://new.example/list.txt ");

    const saveButton = page.locator("#save-subscriptions");
    await expect(saveButton).not.toHaveClass(/inactive/);
    await saveButton.click();

    await expect(page.getByText("Saved")).toBeVisible();
    expect(state.savedBody?.subscriptions[0].url).toBe("https://new.example/list.txt");
  });

  test("prevents saving duplicate subscription URLs", async ({ page }) => {
    await setupSubscriptions(page, [
      makeSubscription("a1b2c3d4", "https://one.example/list.txt", "One"),
      makeSubscription("b1c2d3e4", "https://two.example/list.txt", "Two"),
    ]);

    const urlInputs = page.locator(".subscription-url-input");
    await urlInputs.first().fill("https://two.example/list.txt");

    await expect(urlInputs.first()).toHaveClass(/invalid/);
    await expect(page.locator(".url-error")).toHaveCount(0);
    await expect(page.locator("#save-subscriptions")).toHaveClass(/inactive/);
  });

  test("syncs with the edited unsaved subscription URL", async ({ page }) => {
    const state = await setupSubscriptions(page, [
      makeSubscription("a1b2c3d4", "https://old.example/list.txt"),
    ]);

    await page.locator(".subscription-url-input").first().fill(" https://new.example/list.txt ");
    await page.locator(".action.sync button").first().click();

    expect(state.syncBody?.url).toBe("https://new.example/list.txt");
    await expect(page.locator(".subscription-url-input").first()).toHaveValue(
      "https://new.example/list.txt",
    );
    await expect(page.locator("#save-subscriptions")).toHaveClass(/inactive/);
    await expect(page.locator(".subscription-rule")).toContainText("synced.example.com");
  });

  test("syncing another subscription preserves an unsaved same-ID rule", async ({ page }) => {
    const first = makeSubscription("a1b2c3d4", "https://one.example/list.txt");
    const second = makeSubscription("b1c2d3e4", "https://two.example/list.txt");
    second.rules[0].id = first.rules[0].id;
    const state = await setupSubscriptions(page, [first, second]);
    const panels = page.locator(".subscription-panel");
    await panels.first().locator(".action.collapse button").click();
    await panels.first().locator(".subscription-rule [role=switch]").click();
    await expect(page.locator("#save-subscriptions")).not.toHaveClass(/inactive/);

    await panels.last().locator(".action.sync button").click();
    await expect(panels.last().locator(".subscription-rule")).toContainText("synced.example.com");
    await expect(page.locator("#save-subscriptions")).not.toHaveClass(/inactive/);
    await page.locator("#save-subscriptions").click();
    await expect(page.getByText("Saved", { exact: true })).toBeVisible();
    expect(state.savedBody?.subscriptions[0].rules[0].enable).toBe(false);
    expect(state.savedBody?.subscriptions[1].rules[0].rule).toBe("synced.example.com");
  });
});
