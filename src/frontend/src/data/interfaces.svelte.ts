import { t } from "./locale.svelte";

import { type Interfaces } from "../types";
import { fetcher } from "../utils/fetcher";

export type InterfaceOption = Interfaces["interfaces"][number];

/** Bypasses VPN routing and follows the router's own route; groups only. */
export const DIRECT_INTERFACE = "direct";

export const interfaces = $state({
  list: [] as InterfaceOption[],
});

export async function fetchInterfaces() {
  try {
    const data = await fetcher.get<Interfaces>("/system/interfaces");
    interfaces.list = data.interfaces;
  } catch (error) {
    console.error("Failed to fetch interfaces:", error);
    interfaces.list = [];
  }
}

export function interfaceLabel(id: string) {
  return id === DIRECT_INTERFACE ? t("No VPN") : id;
}

const BLACKHOLE_INTERFACE = "blackhole";

// "No VPN" first, "blackhole" last; real interfaces keep the server's order.
const interfaceRank = (id: string) =>
  id === DIRECT_INTERFACE ? 0 : id === BLACKHOLE_INTERFACE ? 2 : 1;

/** Select options for a routing interface. Subscriptions cannot route directly. */
export function interfaceOptions(includeDirect: boolean) {
  // Array.prototype.sort is stable, so equal ranks keep their order.
  return interfaces.list
    .filter((item) => includeDirect || item.id !== DIRECT_INTERFACE)
    .sort((a, b) => interfaceRank(a.id) - interfaceRank(b.id))
    .map((item) => ({
      value: item.id,
      label: interfaceLabel(item.id),
      description: item.id === DIRECT_INTERFACE ? t("System route") : item.name,
    }));
}
