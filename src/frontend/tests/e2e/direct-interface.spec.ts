import { expect, test } from "@playwright/test";

const interfaces = [
  { id: "blackhole" },
  { id: "direct" },
  { id: "wg1", name: "Second VPN" },
  { id: "wg0", name: "My VPN" },
];

test("groups can bypass VPN routing; subscriptions cannot", async ({ page }) => {
  let savedGroups: { interface: string }[] = [];
  await page.route("**/auth", (route) => route.fulfill({ json: { enabled: false } }));
  await page.route("**/system/interfaces**", (route) => route.fulfill({ json: { interfaces } }));
  await page.route("**/groups?with_rules=true", (route) =>
    route.fulfill({
      json: {
        groups: [
          {
            id: "a1b2c3d4",
            name: "Local sites",
            color: "#ffffff",
            interface: "wg0",
            enable: true,
            rules: [
              { id: "a1b2c3d5", name: "", rule: "example.ru", type: "namespace", enable: true },
            ],
          },
        ],
      },
    }),
  );
  await page.route("**/groups?save=true", async (route) => {
    savedGroups = route.request().postDataJSON().groups;
    await route.fulfill({ json: { groups: savedGroups } });
  });
  await page.route("**/subscriptions", (route) =>
    route.fulfill({
      json: {
        subscriptions: [
          {
            id: "b1b2c3d4",
            name: "ASN",
            url: "https://example.com/asn.txt",
            interface: "wg0",
            enable: true,
            interval: 86400,
            lastUpdate: 0,
            rules: [],
          },
        ],
      },
    }),
  );
  await page.route("**/subscriptions/settings", (route) =>
    route.fulfill({ json: { fetchInterface: "" } }),
  );
  await page.goto("/");

  const optionLabels = () => page.locator('[role="option"] .option-label').allInnerTexts();
  const groupInterface = page.locator(".group-actions [data-select-trigger]").first();
  await groupInterface.click();
  // "No VPN" first, "blackhole" last, tunnels in the server's order.
  await expect.poll(optionLabels).toEqual(["No VPN", "wg1", "wg0", "blackhole"]);
  await page.getByRole("option", { name: /^No VPN/ }).click();
  await expect(groupInterface).toContainText("No VPN");
  await expect(groupInterface).toContainText("System route");
  await page.locator("#save-changes").click();
  await expect.poll(() => savedGroups.map((group) => group.interface)).toEqual(["direct"]);

  await page.getByRole("tab", { name: "Subscriptions" }).click();
  await page.locator(".subscription-interface [data-select-trigger]").first().click();
  await expect.poll(optionLabels).toEqual(["wg1", "wg0", "blackhole"]);
  await expect(page.getByRole("option", { name: /^No VPN/ })).toHaveCount(0);
  await expect(page.getByRole("option", { name: "direct", exact: true })).toHaveCount(0);
});
