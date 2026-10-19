import { t } from "../../data/locale.svelte";
import { ListStore, type VisibleItem } from "../list-store.svelte";

import { type Subscription, type SubscriptionRule } from "../../types";
import { randomId } from "../../utils/defaults";
import { overlay, toast } from "../../utils/events";
import { fetcher } from "../../utils/fetcher";

export const SUBSCRIPTIONS_STORE_CONTEXT = Symbol("subscriptions-store");

export function normalizeSubscriptionUrl(value: string) {
  return value.trim();
}

export function validateSubscriptionUrl(value: string) {
  const normalized = normalizeSubscriptionUrl(value);
  if (!normalized) return false;

  try {
    const url = new URL(normalized);
    return (url.protocol === "http:" || url.protocol === "https:") && Boolean(url.hostname);
  } catch {
    return false;
  }
}

export type SubscriptionDragData = {
  group_id: string;
  group_index: number;
  name: string;
  count: number;
};

export type SubscriptionDropSlotData = {
  group_index: number;
  insert: "before" | "after";
};

type AddSubscriptionPayload = {
  url: string;
  name: string;
  rules: SubscriptionRule[];
  interface: string;
  interval: number;
};

export class SubscriptionsStore extends ListStore<Subscription> {
  subscriptionUrlErrors = $derived.by(() => {
    const errors = new Map<string, string>();
    const idsByUrl = new Map<string, string[]>();

    for (const subscription of this.data) {
      const normalizedUrl = normalizeSubscriptionUrl(subscription.url ?? "");

      if (!validateSubscriptionUrl(normalizedUrl)) {
        errors.set(subscription.id, "Invalid URL");
        continue;
      }

      const ids = idsByUrl.get(normalizedUrl) ?? [];
      ids.push(subscription.id);
      idsByUrl.set(normalizedUrl, ids);
    }

    for (const ids of idsByUrl.values()) {
      if (ids.length <= 1) continue;
      for (const id of ids) {
        errors.set(id, "Subscription already exists");
      }
    }

    return errors;
  });
  valid_subscription_urls = $derived(this.subscriptionUrlErrors.size === 0);
  canSave = $derived(this.tracker.isDirty && this.valid_rules && this.valid_subscription_urls);

  protected readonly itemsName = "subscriptions";

  constructor() {
    super();
    this.setupEffects();
  }

  protected async fetchItems() {
    return (
      (await fetcher.get<{ subscriptions: Subscription[] }>("/subscriptions"))?.subscriptions ?? []
    );
  }

  protected snapshotForSave() {
    return $state.snapshot(this.data).map((subscription) => ({
      ...subscription,
      url: normalizeSubscriptionUrl(subscription.url),
    }));
  }

  protected persist(subscriptions: Subscription[]) {
    return fetcher.put("/subscriptions", { subscriptions });
  }

  performSearch() {
    const query = this.normalizedSearch;
    this.resetForcedVisibility(query);

    if (!query) {
      this.showAllItems();
      this.searchPending = false;
      return;
    }

    const subscriptions = this.data;
    const nextVisible: VisibleItem[] = [];

    for (let i = 0; i < subscriptions.length; i++) {
      const subscription = subscriptions[i];
      const isForcedSubscription = this.forcedItemIds.has(subscription.id);

      const nameMatches = (subscription.name || "").toLowerCase().includes(query);
      const urlMatches = (subscription.url || "").toLowerCase().includes(query);

      if (isForcedSubscription || nameMatches || urlMatches) {
        nextVisible.push({ group_index: i, ruleIndices: null });
        continue;
      }

      const matchedRuleIndices: number[] = [];
      const forcedRules = this.forcedRuleIdsByItem.get(subscription.id);
      const rules = subscription.rules;

      for (let ruleIndex = 0; ruleIndex < rules.length; ruleIndex++) {
        const rule = rules[ruleIndex];
        const isForcedRule = forcedRules?.has(rule.id);
        const patternMatches = (rule.rule || "").toLowerCase().includes(query);
        if (isForcedRule || patternMatches) {
          matchedRuleIndices.push(ruleIndex);
        }
      }

      if (matchedRuleIndices.length > 0) {
        nextVisible.push({ group_index: i, ruleIndices: matchedRuleIndices });
      }
    }

    this.visibleItems = nextVisible;
    this.searchPending = false;
  }

  normalizeSubscriptionUrl(index: number) {
    const subscription = this.data[index];
    if (!subscription) return;

    const normalizedUrl = normalizeSubscriptionUrl(subscription.url);
    if (subscription.url === normalizedUrl) return;

    subscription.url = normalizedUrl;
    this.markDataRevision();
  }

  async syncSubscription(index: number) {
    const subscription = this.data[index];
    if (!subscription) return;

    this.normalizeSubscriptionUrl(index);
    const normalizedUrl = normalizeSubscriptionUrl(subscription.url);
    const urlError = this.subscriptionUrlErrors.get(subscription.id);
    if (urlError) {
      toast.error(t(urlError));
      return;
    }

    overlay.show(t("syncing..."));
    try {
      const updated = await fetcher.post<{
        rules: SubscriptionRule[];
        lastUpdate: number;
        url?: string;
      }>(`/subscriptions/${subscription.id}/sync`, { url: normalizedUrl });

      this.tracker.acknowledgeUpdate(subscription, {
        url: updated.url ?? normalizedUrl,
        rules: updated.rules,
        lastUpdate: updated.lastUpdate,
      });

      this.markDataRevision();
      toast.success(t("Synced"));
    } catch (error) {
      console.error(error);
      toast.error(t("Failed to sync"));
    } finally {
      overlay.hide();
    }
  }

  async deleteSubscription(index: number, confirmed = false) {
    if (!confirmed && !confirm(t("Delete this subscription?"))) return;

    const removed = this.data[index];
    if (!removed) return;

    overlay.show(t("deleting subscription..."));
    try {
      await fetcher.delete(`/subscriptions/${removed.id}`);
      this.data.splice(index, 1);
      this.tracker.acknowledgeDelete(this.data, removed.id);
      delete this.open_state[removed.id];
      this.removeForcedGroup(removed.id);
      this.markDataRevision();
      toast.success(t("Deleted"));
    } catch (error) {
      console.error(error);
      toast.error(t("Failed to delete subscription"));
    } finally {
      overlay.hide();
    }
  }

  async addSubscription(payload: AddSubscriptionPayload) {
    const nextSubscription: Subscription = {
      id: randomId(),
      name: payload.name,
      url: payload.url,
      rules: payload.rules,
      enable: true,
      interface: payload.interface || "",
      lastUpdate: Math.floor(Date.now() / 1000),
      interval: payload.interval,
    };

    overlay.show(t("Adding..."));
    try {
      await fetcher.post("/subscriptions", nextSubscription);
      this.data.unshift(nextSubscription);
      this.tracker.acknowledgeNewItem(this.data, nextSubscription, "start");
      this.open_state[nextSubscription.id] = true;
      if (this.searchActive) {
        this.forceVisibleGroup(nextSubscription.id);
      }
      this.markDataRevision();
      toast.success(t("Added"));
    } catch (error) {
      console.error(error);
      toast.error(t("Failed to add subscription"));
    } finally {
      overlay.hide();
    }
  }

  handleSubscriptionSlotDrop = (source: SubscriptionDragData, target: SubscriptionDropSlotData) => {
    const { group_index: from_index } = source;
    const { group_index: to_index, insert } = target;
    this.changeIndex(from_index, to_index, insert);
  };
}
