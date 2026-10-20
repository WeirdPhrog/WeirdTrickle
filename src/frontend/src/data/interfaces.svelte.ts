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

/** Select options for a routing interface. Subscriptions cannot route directly. */
export function interfaceOptions(includeDirect: boolean) {
  return interfaces.list
    .filter((item) => includeDirect || item.id !== DIRECT_INTERFACE)
    .map((item) => ({
      value: item.id,
      label: interfaceLabel(item.id),
      description: item.id === DIRECT_INTERFACE ? t("System route") : item.name,
    }));
}
