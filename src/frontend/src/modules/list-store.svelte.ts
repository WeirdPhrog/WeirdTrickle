import { t } from "../data/locale.svelte";
import { ChangeTracker } from "../utils/change-tracker.svelte";

import { overlay, toast } from "../utils/events";
import { isValidRule } from "../utils/rule-validators";

const SEARCH_DEBOUNCE_MS = 150 as const;

export const clamp = (value: number, min: number, max: number) =>
  Math.max(min, Math.min(max, value));

export type VisibleItem = {
  group_index: number;
  ruleIndices: number[] | null;
};

type ListItem = {
  id: string;
  rules: { id: string; enable: boolean; type: string; rule: string }[];
};

/**
 * Shared state and lifecycle of the groups and subscriptions pages: loading,
 * change tracking, debounced search with forced visibility, progressive
 * rendering, reordering and saving. Subclasses call setupEffects() from their
 * constructor, after their own fields exist.
 */
export abstract class ListStore<T extends ListItem> {
  tracker = $state(new ChangeTracker<T[]>([]));
  data = $derived.by(() => this.tracker.data);
  dataRevision = $state(0);
  valid_rules = $state(true);

  open_state = $state<Record<string, boolean>>({});

  searchValue = $state("");
  visibleItems = $state<VisibleItem[]>([]);
  searchPending = $state(false);

  normalizedSearch = $derived(this.searchValue.trim().toLowerCase());
  searchActive = $derived(Boolean(this.normalizedSearch));

  visibilityMap = $derived(new Map(this.visibleItems.map((v) => [v.group_index, v.ruleIndices])));

  firstVisibleIndex = $derived(
    this.searchActive ? (this.visibleItems.length ? this.visibleItems[0].group_index : -1) : 0,
  );

  noVisibleItems = $derived(
    this.searchActive && !this.searchPending && this.visibleItems.length === 0,
  );

  finishedCount = $state(0);
  fetchError = $state(false);
  dataLoaded = $state(false);

  isAllRendered = $derived(
    this.dataLoaded &&
      (this.data.length === 0 || this.finishedCount >= this.data.length || this.searchActive),
  );

  isEmptyData = $derived(
    this.dataLoaded &&
      !this.fetchError &&
      !this.searchActive &&
      !this.searchPending &&
      this.data.length === 0,
  );

  renderLimit = $state(1);
  renderTimeout: number | null = null;

  protected forcedItemIds = new Set<string>();
  protected forcedRuleIdsByItem = new Map<string, Set<string>>();
  #forcedSearchKey = "";
  #debounceTimer: number | null = null;
  #dispose: (() => void) | null = null;

  abstract canSave: boolean;
  /** Plural noun used in load error logs. */
  protected abstract readonly itemsName: string;
  protected abstract fetchItems(): Promise<T[]>;
  protected abstract persist(items: T[]): Promise<unknown>;
  abstract performSearch(): void;

  /** Called when the search is cleared, before all items become visible. */
  protected onSearchCleared() {}
  /** Called when a non-empty search is scheduled. */
  protected onSearchQueued() {}
  /** Called after each data revision. */
  protected onDataRevision() {}
  /** Called after loading, whether or not it succeeded. */
  protected afterLoad() {}
  /** Clears subclass timers when the store is destroyed. */
  protected disposeTimers() {}

  protected snapshotForSave(): T[] {
    return $state.snapshot(this.data) as T[];
  }

  protected setupEffects() {
    this.#dispose = $effect.root(() => {
      $effect(() => {
        if (typeof window === "undefined" || !this.tracker.isDirty) return;

        const handleBeforeUnload = (event: BeforeUnloadEvent) => {
          event.preventDefault();
        };

        window.addEventListener("beforeunload", handleBeforeUnload);
        return () => window.removeEventListener("beforeunload", handleBeforeUnload);
      });

      $effect(() => {
        this.dataRevision;
        if (typeof window === "undefined") return;
        const timer = setTimeout(() => this.checkRulesValidityState(), 10);
        return () => clearTimeout(timer);
      });

      $effect(() => {
        const query = this.normalizedSearch;
        this.dataRevision;
        this.data.length;

        if (this.#debounceTimer) {
          clearTimeout(this.#debounceTimer);
          this.#debounceTimer = null;
        }

        if (!query) {
          this.onSearchCleared();
          this.showAllItems();
          this.searchPending = false;
          return;
        }

        this.searchPending = true;
        this.onSearchQueued();

        if (typeof window === "undefined") {
          this.performSearch();
          return;
        }

        this.#debounceTimer = window.setTimeout(() => this.performSearch(), SEARCH_DEBOUNCE_MS);
      });

      $effect(() => {
        if (this.searchActive) {
          this.renderLimit = this.data.length;
          if (this.renderTimeout) {
            clearTimeout(this.renderTimeout);
            this.renderTimeout = null;
          }
        } else {
          this.scheduleRenderNext();
        }
      });

      $effect(() => {
        this.data.length;
        this.scheduleRenderNext();
      });

      return () => {
        if (this.#debounceTimer) {
          clearTimeout(this.#debounceTimer);
          this.#debounceTimer = null;
        }

        this.disposeTimers();

        if (this.renderTimeout) {
          clearTimeout(this.renderTimeout);
          this.renderTimeout = null;
        }
      };
    });
  }

  destroy() {
    if (typeof window !== "undefined") {
      window.removeEventListener("keydown", this.handleSaveShortcut);
    }

    if (this.#dispose) {
      this.#dispose();
      this.#dispose = null;
    }
  }

  mount = async () => {
    this.finishedCount = 0;
    this.fetchError = false;
    try {
      this.tracker = new ChangeTracker(await this.fetchItems());
      this.dataRevision = 0;
      if (typeof window !== "undefined") {
        setTimeout(() => this.checkRulesValidityState(), 10);
      }
    } catch (error) {
      this.fetchError = true;
      console.error(`Failed to load ${this.itemsName}:`, error);
    } finally {
      this.afterLoad();
      this.dataLoaded = true;
    }

    if (typeof window !== "undefined") {
      window.addEventListener("keydown", this.handleSaveShortcut);
    }
  };

  handleSaveShortcut = (event: KeyboardEvent) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
      if (this.canSave) {
        event.preventDefault();
        this.saveChanges();
      }
    }
  };

  saveChanges() {
    this.checkRulesValidityState();
    if (!this.canSave) return;
    overlay.show(t("saving changes..."));

    const rawData = this.snapshotForSave();

    this.persist(rawData)
      .then(() => {
        this.tracker.reset(rawData);
        overlay.hide();
        toast.success(t("Saved"));
      })
      .catch(() => {
        overlay.hide();
      });
  }

  checkRulesValidityState = () => {
    this.valid_rules = this.data.every((item) =>
      item.rules.every((rule) => !rule.enable || isValidRule(rule)),
    );
  };

  markDataRevision = () => {
    this.dataRevision += 1;
    this.onDataRevision();
  };

  protected showAllItems() {
    this.visibleItems = this.data.map((_, index) => ({
      group_index: index,
      ruleIndices: null,
    }));
  }

  protected resetForcedVisibility(searchKey: string) {
    if (!this.#forcedSearchKey) return;
    if (this.#forcedSearchKey !== searchKey) {
      this.forcedItemIds.clear();
      this.forcedRuleIdsByItem.clear();
      this.#forcedSearchKey = "";
    }
  }

  forceVisibleGroup(groupId: string) {
    if (!this.normalizedSearch) return;
    this.forcedItemIds.add(groupId);
    this.#forcedSearchKey = this.normalizedSearch;
  }

  forceVisibleRule(groupId: string, ruleId: string) {
    if (!this.normalizedSearch) return;
    let forced = this.forcedRuleIdsByItem.get(groupId);
    if (!forced) {
      forced = new Set<string>();
      this.forcedRuleIdsByItem.set(groupId, forced);
    }
    forced.add(ruleId);
    this.#forcedSearchKey = this.normalizedSearch;
  }

  removeForcedGroup(groupId: string) {
    this.forcedItemIds.delete(groupId);
    this.forcedRuleIdsByItem.delete(groupId);
  }

  removeForcedRule(groupId: string, ruleId: string) {
    const forced = this.forcedRuleIdsByItem.get(groupId);
    if (!forced) return;
    forced.delete(ruleId);
    if (!forced.size) this.forcedRuleIdsByItem.delete(groupId);
  }

  moveForcedRule(
    sourceGroupId: string,
    targetGroupId: string,
    ruleId: string,
    targetRuleId = ruleId,
  ) {
    if (sourceGroupId === targetGroupId) return;
    const forced = this.forcedRuleIdsByItem.get(sourceGroupId);
    if (!forced?.has(ruleId)) return;
    forced.delete(ruleId);
    if (!forced.size) this.forcedRuleIdsByItem.delete(sourceGroupId);
    let targetForced = this.forcedRuleIdsByItem.get(targetGroupId);
    if (!targetForced) {
      targetForced = new Set<string>();
      this.forcedRuleIdsByItem.set(targetGroupId, targetForced);
    }
    targetForced.add(targetRuleId);
  }

  changeIndex(from_index: number, to_index: number, insert: "before" | "after" = "before") {
    if (from_index === to_index && insert !== "after") return;
    if (from_index < 0 || from_index >= this.data.length) return;

    const item = this.data[from_index];
    if (!item) return;

    this.data.splice(from_index, 1);

    let target = insert === "after" ? to_index + 1 : to_index;
    if (from_index < target) target -= 1;

    target = clamp(target, 0, this.data.length);
    this.data.splice(target, 0, item);

    this.markDataRevision();
  }

  handleItemFinished = () => {
    this.finishedCount += 1;
  };

  scheduleRenderNext() {
    if (typeof window === "undefined") return;
    if (this.renderTimeout) return;
    if (this.renderLimit >= this.data.length) return;

    this.renderTimeout = window.setTimeout(() => {
      this.renderTimeout = null;
      if (this.searchActive) {
        this.renderLimit = this.data.length;
        return;
      }
      this.renderLimit = Math.min(this.renderLimit + 2, this.data.length);
      this.scheduleRenderNext();
    }, 15);
  }
}
