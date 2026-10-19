import { tick } from "svelte";

import { t } from "../../data/locale.svelte";
import { clamp, ListStore, type VisibleItem } from "../list-store.svelte";

import { type Group, type Rule } from "../../types";
import { defaultGroup, defaultRule, randomId } from "../../utils/defaults";
import { fetcher } from "../../utils/fetcher";
import { type SortDirection, type SortField } from "../../utils/rule-sorter";
import { isValidRule } from "../../utils/rule-validators";
import {
  cloneGroupsWithNewIds as cloneGroupsWithNewIdsData,
  cloneGroupWithNewIds as cloneGroupWithNewIdsData,
  prependGroups as prependGroupsData,
  prependRules as prependRulesData,
  restoreGroupRulesOrder as restoreGroupRulesOrderData,
  sortGroupRules as sortGroupRulesData,
  toConfigPayload as toConfigPayloadData,
} from "./groups-data";
import { captureRuleMove } from "./rule-motion";

export const GROUPS_STORE_CONTEXT = Symbol("groups-store");

const IMPORT_RULES_CHUNK_SIZE = 300 as const;
const IMPORT_GROUPS_CLONE_CHUNK_SIZE = 20 as const;
const IMPORT_GROUPS_INSERT_CHUNK_SIZE = 25 as const;
export const RULE_SEARCH_MATCH_NAME = 1 << 0;
export const RULE_SEARCH_MATCH_PATTERN = 1 << 1;

export type GroupDragData = {
  group_id: string;
  group_index: number;
  name: string;
  color: string;
  count: number;
};

export type GroupDropSlotData = {
  group_index: number;
  insert: "before" | "after";
};

export type GroupDuplicateConflict = {
  ruleId: string;
  ruleName: string;
  rulePattern: string;
  ruleType: string;
  groupId: string;
  groupName: string;
  groupColor: string;
  inCurrentGroup: boolean;
  duplicateKey: string;
  totalRulesWithSameKey: number;
};

export type DuplicateRuleFocusRequest = {
  groupId: string;
  ruleId: string;
  nonce: number;
};

type DuplicateComputationResult = {
  duplicateRules: Set<string>;
  duplicateGroups: Set<string>;
  duplicateRuleKeys: Map<string, string>;
  duplicateConflictsByGroup: Map<string, GroupDuplicateConflict[]>;
};

type SearchIndexRule = {
  id: string;
  nameLower: string;
  patternLower: string;
};

type SearchIndexGroup = {
  id: string;
  nameLower: string;
  rules: SearchIndexRule[];
};

type SearchHighlightSegment = {
  text: string;
  matched: boolean;
};

export class GroupsStore extends ListStore<Group> {
  canSave = $derived(this.tracker.isDirty && this.valid_rules);

  searchMatchedGroupIds = $state<Set<string>>(new Set());
  searchRuleMatchMaskById = $state<Map<string, number>>(new Map());

  searchIndex = $state<SearchIndexGroup[]>([]);
  searchIndexRevision = $state(-1);

  duplicateRuleIds = $state<Set<string>>(new Set());
  duplicateGroupIds = $state<Set<string>>(new Set());
  duplicateRuleKeys = $state<Map<string, string>>(new Map());
  duplicateConflictsByGroup = $state<Map<string, GroupDuplicateConflict[]>>(new Map());
  activeDuplicateKey = $state<string | null>(null);
  duplicateHighlightPinned = $state(false);
  highlightedRuleId = $state<string | null>(null);
  duplicateFocusRequest = $state<DuplicateRuleFocusRequest | null>(null);

  #duplicateRuleIdsTimer: number | null = null;
  #highlightedRuleTimer: number | null = null;
  #searchIndexBuildToken = 0;
  #searchIndexBuilding = false;

  protected readonly itemsName = "groups";

  constructor() {
    super();
    this.setupEffects();
  }

  protected async fetchItems() {
    return (await fetcher.get<{ groups: Group[] }>("/groups?with_rules=true"))?.groups ?? [];
  }

  protected persist(groups: Group[]) {
    return fetcher.put("/groups?save=true", { groups });
  }

  protected afterLoad() {
    this.refreshDuplicateRuleIds();
  }

  protected onSearchCleared() {
    this.#cancelSearchIndexBuild();
    this.#clearSearchMatches();
  }

  protected onSearchQueued() {
    if (this.searchIndexRevision !== this.dataRevision) {
      this.#startSearchIndexBuild();
    }
  }

  protected onDataRevision() {
    this.scheduleDuplicateRuleIdsRefresh();
  }

  protected disposeTimers() {
    if (this.#duplicateRuleIdsTimer) {
      clearTimeout(this.#duplicateRuleIdsTimer);
      this.#duplicateRuleIdsTimer = null;
    }

    if (this.#highlightedRuleTimer) {
      clearTimeout(this.#highlightedRuleTimer);
      this.#highlightedRuleTimer = null;
    }
  }

  syncRuleDeletion(groupIndex: number, ruleIndex: number) {
    if (!this.searchActive) return;
    const targetIndex = this.visibleItems.findIndex((entry) => entry.group_index === groupIndex);
    if (targetIndex === -1) return;
    const target = this.visibleItems[targetIndex];
    if (!target?.ruleIndices) return;

    const ruleIndices = target.ruleIndices;
    let write = 0;
    for (let i = 0; i < ruleIndices.length; i++) {
      const index = ruleIndices[i];
      if (index === ruleIndex) continue;
      ruleIndices[write] = index > ruleIndex ? index - 1 : index;
      write += 1;
    }

    if (write === 0) {
      this.visibleItems.splice(targetIndex, 1);
      return;
    }

    ruleIndices.length = write;
    target.ruleIndices = ruleIndices;
    this.visibleItems[targetIndex] = target;
  }

  #clearSearchMatches() {
    this.searchMatchedGroupIds = new Set();
    this.searchRuleMatchMaskById = new Map();
  }

  #splitSearchHighlightSegments(
    value: string,
    query: string,
  ): SearchHighlightSegment[] | undefined {
    if (!value || !query) return undefined;

    const source = `${value}`;
    const needle = query.trim().toLowerCase();
    if (!needle) return undefined;

    const haystack = source.toLowerCase();
    const needleLength = needle.length;

    let cursor = 0;
    let matchIndex = haystack.indexOf(needle, cursor);
    if (matchIndex === -1) return undefined;

    const segments: SearchHighlightSegment[] = [];

    while (matchIndex !== -1) {
      if (matchIndex > cursor) {
        segments.push({ text: source.slice(cursor, matchIndex), matched: false });
      }

      const matchEnd = matchIndex + needleLength;
      segments.push({ text: source.slice(matchIndex, matchEnd), matched: true });

      cursor = matchEnd;
      matchIndex = haystack.indexOf(needle, cursor);
    }

    if (cursor < source.length) {
      segments.push({ text: source.slice(cursor), matched: false });
    }

    return segments;
  }

  #cancelSearchIndexBuild() {
    this.#searchIndexBuildToken += 1;
  }

  #startSearchIndexBuild(incrementToken = true) {
    if (incrementToken) {
      this.#searchIndexBuildToken += 1;
    }

    if (this.#searchIndexBuilding) return;
    void this.#buildSearchIndex(this.#searchIndexBuildToken);
  }

  async #buildSearchIndex(token: number) {
    if (!this.searchActive) return;

    this.#searchIndexBuilding = true;
    const revision = this.dataRevision;
    const groups = this.data;
    const total = groups.length;
    const nextIndex = new Array<SearchIndexGroup>(total);

    const now = () => (typeof performance !== "undefined" ? performance.now() : Date.now());
    let lastYield = now();

    const maybeYield = async () => {
      if (now() - lastYield < 8) return;
      await this.#yieldToMain();
      lastYield = now();
    };

    const abortIfStale = () => token !== this.#searchIndexBuildToken || !this.searchActive;

    for (let i = 0; i < total; i++) {
      if (abortIfStale()) {
        this.#searchIndexBuilding = false;
        if (this.searchActive && token !== this.#searchIndexBuildToken) {
          this.#startSearchIndexBuild(false);
        }
        return;
      }

      const group = groups[i];
      const rules = group.rules;
      const rulesCount = rules.length;
      const indexedRules = new Array<SearchIndexRule>(rulesCount);

      for (let r = 0; r < rulesCount; r++) {
        const rule = rules[r];
        indexedRules[r] = {
          id: rule.id,
          nameLower: (rule.name || "").toLowerCase(),
          patternLower: (rule.rule || "").toLowerCase(),
        };
        if ((r & 31) === 0) {
          await maybeYield();
          if (abortIfStale()) {
            this.#searchIndexBuilding = false;
            if (this.searchActive && token !== this.#searchIndexBuildToken) {
              this.#startSearchIndexBuild(false);
            }
            return;
          }
        }
      }

      nextIndex[i] = {
        id: group.id,
        nameLower: (group.name || "").toLowerCase(),
        rules: indexedRules,
      };

      if ((i & 7) === 0) {
        await maybeYield();
      }
    }

    if (abortIfStale()) {
      this.#searchIndexBuilding = false;
      if (this.searchActive && token !== this.#searchIndexBuildToken) {
        this.#startSearchIndexBuild(false);
      }
      return;
    }

    if (this.dataRevision !== revision) {
      this.#searchIndexBuilding = false;
      if (this.searchActive) {
        this.#startSearchIndexBuild(false);
      }
      return;
    }

    this.searchIndex = nextIndex;
    this.searchIndexRevision = revision;
    this.#searchIndexBuilding = false;

    if (this.normalizedSearch) {
      this.performSearch();
    } else {
      this.searchPending = false;
    }
  }

  performSearch() {
    const query = this.normalizedSearch;
    this.resetForcedVisibility(query);

    if (!query) {
      this.showAllItems();
      this.#clearSearchMatches();
      this.searchPending = false;
      return;
    }

    if (this.searchIndexRevision !== this.dataRevision) {
      this.#clearSearchMatches();
      this.searchPending = true;
      return;
    }

    const nextVisible: VisibleItem[] = [];
    const matchedGroupIds = new Set<string>();
    const ruleMatchMaskById = new Map<string, number>();
    const searchIndex = this.searchIndex;
    const len = searchIndex.length;

    for (let i = 0; i < len; i++) {
      const indexedGroup = searchIndex[i];
      const isForcedGroup = this.forcedItemIds.has(indexedGroup.id);
      const isGroupMatchedByName = indexedGroup.nameLower.includes(query);

      if (isForcedGroup || isGroupMatchedByName) {
        if (isGroupMatchedByName) {
          matchedGroupIds.add(indexedGroup.id);
        }
        nextVisible.push({ group_index: i, ruleIndices: null });
        continue;
      }

      const matchedRuleIndices: number[] = [];
      const forcedRules = this.forcedRuleIdsByItem.get(indexedGroup.id);
      const rules = indexedGroup.rules;
      const rulesLen = rules.length;

      for (let r = 0; r < rulesLen; r++) {
        const indexedRule = rules[r];
        let matchMask = 0;
        if (indexedRule.nameLower.includes(query)) {
          matchMask |= RULE_SEARCH_MATCH_NAME;
        }
        if (indexedRule.patternLower.includes(query)) {
          matchMask |= RULE_SEARCH_MATCH_PATTERN;
        }

        if (forcedRules?.has(indexedRule.id) || matchMask !== 0) {
          matchedRuleIndices.push(r);
          if (matchMask !== 0) {
            ruleMatchMaskById.set(indexedRule.id, matchMask);
          }
        }
      }

      if (matchedRuleIndices.length > 0) {
        nextVisible.push({ group_index: i, ruleIndices: matchedRuleIndices });
      }
    }

    this.visibleItems = nextVisible;
    this.searchMatchedGroupIds = matchedGroupIds;
    this.searchRuleMatchMaskById = ruleMatchMaskById;
    this.searchPending = false;
  }

  #sortDuplicateConflicts(conflicts: GroupDuplicateConflict[]) {
    conflicts.sort((a, b) => {
      const byKey = a.duplicateKey.localeCompare(b.duplicateKey);
      if (byKey !== 0) return byKey;

      if (a.inCurrentGroup !== b.inCurrentGroup) {
        return a.inCurrentGroup ? 1 : -1;
      }
      const byGroup = (a.groupName || "").localeCompare(b.groupName || "");
      if (byGroup !== 0) return byGroup;
      const byName = (a.ruleName || "").localeCompare(b.ruleName || "");
      if (byName !== 0) return byName;
      const byPattern = (a.rulePattern || "").localeCompare(b.rulePattern || "");
      if (byPattern !== 0) return byPattern;
      return a.ruleId.localeCompare(b.ruleId);
    });
  }

  #computeDuplicates(): DuplicateComputationResult {
    const duplicateRules = new Set<string>();
    const duplicateGroups = new Set<string>();
    const duplicateRuleKeys = new Map<string, string>();
    const rulesByKey = new Map<
      string,
      { rule: Rule; groupId: string; groupName: string; groupColor: string }[]
    >();

    for (const group of this.data) {
      for (const rule of group.rules) {
        const normalizedPattern = rule.rule.trim();
        if (!normalizedPattern) continue;

        const key = `${rule.type}:${rule.rule}`;
        duplicateRuleKeys.set(rule.id, key);
        let rules = rulesByKey.get(key);
        if (!rules) {
          rules = [];
          rulesByKey.set(key, rules);
        }
        rules.push({
          rule,
          groupId: group.id,
          groupName: group.name,
          groupColor: group.color || "#ffffff",
        });
      }
    }

    const conflictsByKey = new Map<string, GroupDuplicateConflict[]>();
    const duplicateKeysByGroup = new Map<string, Set<string>>();

    for (const [key, rules] of rulesByKey) {
      if (rules.length < 2) continue;

      for (const entry of rules) {
        duplicateRules.add(entry.rule.id);
        duplicateGroups.add(entry.groupId);
        let keys = duplicateKeysByGroup.get(entry.groupId);
        if (!keys) {
          keys = new Set<string>();
          duplicateKeysByGroup.set(entry.groupId, keys);
        }
        keys.add(key);
      }

      conflictsByKey.set(
        key,
        rules.map((entry) => ({
          ruleId: entry.rule.id,
          ruleName: entry.rule.name,
          rulePattern: entry.rule.rule,
          ruleType: entry.rule.type,
          groupId: entry.groupId,
          groupName: entry.groupName,
          groupColor: entry.groupColor,
          inCurrentGroup: false,
          duplicateKey: key,
          totalRulesWithSameKey: rules.length,
        })),
      );
    }

    const duplicateConflictsByGroup = new Map<string, GroupDuplicateConflict[]>();

    for (const [groupId, keys] of duplicateKeysByGroup) {
      const conflicts: GroupDuplicateConflict[] = [];
      for (const key of keys) {
        const sameKeyConflicts = conflictsByKey.get(key);
        if (!sameKeyConflicts) continue;
        for (const conflict of sameKeyConflicts) {
          conflicts.push({
            ...conflict,
            inCurrentGroup: conflict.groupId === groupId,
          });
        }
      }
      this.#sortDuplicateConflicts(conflicts);
      duplicateConflictsByGroup.set(groupId, conflicts);
    }

    return { duplicateRules, duplicateGroups, duplicateRuleKeys, duplicateConflictsByGroup };
  }

  #applyDuplicateComputation(result: DuplicateComputationResult) {
    this.duplicateRuleIds = result.duplicateRules;
    this.duplicateGroupIds = result.duplicateGroups;
    this.duplicateRuleKeys = result.duplicateRuleKeys;
    this.duplicateConflictsByGroup = result.duplicateConflictsByGroup;
    this.#syncActiveDuplicateKey();
  }

  #syncActiveDuplicateKey() {
    if (this.highlightedRuleId && !this.duplicateRuleIds.has(this.highlightedRuleId)) {
      this.highlightedRuleId = null;
    }

    if (!this.activeDuplicateKey) return;

    for (const ruleId of this.duplicateRuleIds) {
      if (this.duplicateRuleKeys.get(ruleId) === this.activeDuplicateKey) {
        return;
      }
    }

    this.activeDuplicateKey = null;
    this.duplicateHighlightPinned = false;
  }

  #resolveDuplicateKey(ruleId: string) {
    const key = this.duplicateRuleKeys.get(ruleId);
    if (!key || !this.duplicateRuleIds.has(ruleId)) {
      return null;
    }
    return key;
  }

  refreshDuplicateRuleIds = () => {
    if (this.#duplicateRuleIdsTimer) {
      clearTimeout(this.#duplicateRuleIdsTimer);
      this.#duplicateRuleIdsTimer = null;
    }
    this.#applyDuplicateComputation(this.#computeDuplicates());
  };

  scheduleDuplicateRuleIdsRefresh = (delayMs = 120) => {
    if (typeof window === "undefined") {
      this.refreshDuplicateRuleIds();
      return;
    }

    if (this.#duplicateRuleIdsTimer) {
      clearTimeout(this.#duplicateRuleIdsTimer);
    }

    this.#duplicateRuleIdsTimer = window.setTimeout(() => {
      this.#duplicateRuleIdsTimer = null;
      this.#applyDuplicateComputation(this.#computeDuplicates());
    }, delayMs);
  };

  isRuleDuplicate = (ruleId: string) => this.duplicateRuleIds.has(ruleId);
  getRuleSearchMatchMask = (ruleId: string) => this.searchRuleMatchMaskById.get(ruleId) ?? 0;
  getSearchHighlightParts = (value: string, query: string): SearchHighlightSegment[] | undefined =>
    this.#splitSearchHighlightSegments(value, query);

  pinDuplicateByRuleId = (ruleId: string) => {
    const key = this.#resolveDuplicateKey(ruleId);
    if (!key) {
      this.activeDuplicateKey = null;
      this.duplicateHighlightPinned = false;
      return false;
    }
    this.activeDuplicateKey = key;
    this.duplicateHighlightPinned = true;
    return true;
  };

  setActiveDuplicateByRuleId = (ruleId: string) => {
    if (this.duplicateHighlightPinned) return;
    const key = this.#resolveDuplicateKey(ruleId);
    if (!key) {
      this.activeDuplicateKey = null;
      return;
    }
    this.activeDuplicateKey = key;
  };

  clearActiveDuplicateByRuleId = (ruleId: string) => {
    if (this.duplicateHighlightPinned || !this.activeDuplicateKey) return;
    const key = this.#resolveDuplicateKey(ruleId);
    if (!key || key === this.activeDuplicateKey) {
      this.activeDuplicateKey = null;
    }
  };

  togglePinnedDuplicateByRuleId = (ruleId: string) => {
    const key = this.#resolveDuplicateKey(ruleId);
    if (!key) {
      this.activeDuplicateKey = null;
      this.duplicateHighlightPinned = false;
      return;
    }
    if (this.duplicateHighlightPinned && this.activeDuplicateKey === key) {
      this.activeDuplicateKey = null;
      this.duplicateHighlightPinned = false;
      return;
    }
    this.pinDuplicateByRuleId(ruleId);
  };

  clearPinnedDuplicateHighlight = () => {
    if (!this.duplicateHighlightPinned) return;
    this.activeDuplicateKey = null;
    this.duplicateHighlightPinned = false;
  };

  highlightRuleTemporarily = (ruleId: string, durationMs = 2200) => {
    if (!this.duplicateRuleIds.has(ruleId)) return false;

    if (this.#highlightedRuleTimer) {
      clearTimeout(this.#highlightedRuleTimer);
      this.#highlightedRuleTimer = null;
    }

    const applyHighlight = () => {
      this.highlightedRuleId = ruleId;

      if (typeof window === "undefined") return;

      this.#highlightedRuleTimer = window.setTimeout(() => {
        this.#highlightedRuleTimer = null;
        if (this.highlightedRuleId === ruleId) {
          this.highlightedRuleId = null;
        }
      }, durationMs);
    };

    if (this.highlightedRuleId === ruleId) {
      this.highlightedRuleId = null;
      if (typeof window !== "undefined") {
        requestAnimationFrame(() => applyHighlight());
      } else {
        applyHighlight();
      }
      return true;
    }

    applyHighlight();

    return true;
  };

  isRuleHighlighted = (ruleId: string) => this.highlightedRuleId === ruleId;

  isRuleHighlightPinned = (ruleId: string) => this.isRuleHighlighted(ruleId);

  requestDuplicateRuleFocus = (groupId: string, ruleId: string) => {
    const groupIndex = this.data.findIndex((group) => group.id === groupId);
    if (groupIndex < 0) return false;

    if (this.searchActive) {
      this.forceVisibleRule(groupId, ruleId);
      this.performSearch();
    }

    this.renderLimit = Math.max(this.renderLimit, groupIndex + 1);
    this.open_state[groupId] = true;

    const nonce = (this.duplicateFocusRequest?.nonce ?? 0) + 1;
    this.duplicateFocusRequest = { groupId, ruleId, nonce };
    return true;
  };

  consumeDuplicateRuleFocus = (groupId: string, ruleId: string, nonce: number) => {
    const request = this.duplicateFocusRequest;
    if (!request) return;
    if (request.groupId !== groupId || request.ruleId !== ruleId || request.nonce !== nonce) return;
    this.duplicateFocusRequest = null;
  };

  getDuplicateConflictsForGroup = (groupId: string): GroupDuplicateConflict[] =>
    this.duplicateConflictsByGroup.get(groupId) ?? [];

  async addRuleToGroup(group_index: number, rule: Rule, focus = false) {
    const group = this.data[group_index];
    if (!group) return;
    group.rules.unshift(rule);
    this.markDataRevision();
    if (!isValidRule(rule)) {
      this.valid_rules = false;
    }
    if (this.searchActive) {
      this.forceVisibleRule(group.id, rule.id);
    }
    if (!focus) return;
    await tick();
    const el = document.querySelector(
      `.rule[data-group-uuid="${group.id}"][data-uuid="${rule.id}"]`,
    );
    if (el) {
      el.querySelector<HTMLInputElement>("div.name input")?.focus();
      el.querySelector<HTMLInputElement>("div.pattern input")?.classList.add("invalid");
      this.checkRulesValidityState();
    }
  }

  deleteRuleFromGroup = (group_index: number, rule_index: number) => {
    const group = this.data[group_index];
    if (!group) return;
    const removed = group.rules[rule_index];
    group.rules.splice(rule_index, 1);
    if (removed) {
      this.removeForcedRule(group.id, removed.id);
    }
    this.syncRuleDeletion(group_index, rule_index);
    this.markDataRevision();
  };

  changeRuleIndex(
    from_group_index: number,
    from_rule_index: number,
    to_group_index: number,
    to_rule_index: number,
    to_rule_id?: string,
    insert: "before" | "after" = "before",
  ) {
    const sourceGroup = this.data[from_group_index];
    const targetGroup = this.data[to_group_index];

    if (!sourceGroup || !targetGroup) return;

    const isSameGroup = from_group_index === to_group_index;

    const sourceRules = sourceGroup.rules;
    const targetRules = targetGroup.rules;

    if (!sourceRules.length) return;

    const fromIndex = clamp(from_rule_index, 0, sourceRules.length - 1);
    const animateMove = captureRuleMove(sourceRules[fromIndex].id);
    const [movedRule] = sourceRules.splice(fromIndex, 1);
    if (!movedRule) return;

    if (!isSameGroup) {
      const sourceId = movedRule.id;
      // IDs may overlap across groups, but must stay unique within the target.
      if (targetRules.some((rule) => rule.id === sourceId)) {
        const targetIds = new Set(targetRules.map((rule) => rule.id));
        do {
          movedRule.id = randomId();
        } while (targetIds.has(movedRule.id));
      }
      this.moveForcedRule(sourceGroup.id, targetGroup.id, sourceId, movedRule.id);
    }

    let anchorIndex =
      to_rule_id && to_rule_id.length > 0 ? targetRules.findIndex((r) => r.id === to_rule_id) : -1;

    if (anchorIndex === -1 && targetRules.length > 0) {
      anchorIndex = clamp(to_rule_index, 0, targetRules.length - 1);
      if (isSameGroup && fromIndex < anchorIndex) {
        anchorIndex -= 1;
      }
    }

    let insertIndex: number;
    if (anchorIndex === -1) {
      insertIndex = insert === "after" ? targetRules.length : 0;
    } else {
      insertIndex = insert === "after" ? anchorIndex + 1 : anchorIndex;
    }

    insertIndex = clamp(insertIndex, 0, targetRules.length);
    targetRules.splice(insertIndex, 0, movedRule);

    this.markDataRevision();
    void animateMove();
  }

  canDropGroup = (source: GroupDragData, target: GroupDropSlotData) => {
    const slot = target.group_index + (target.insert === "after" ? 1 : 0);
    return source.group_index !== slot && source.group_index + 1 !== slot;
  };

  handleGroupSlotDrop = (source: GroupDragData, target: GroupDropSlotData) => {
    if (!this.canDropGroup(source, target)) return;
    const { group_index: from_index } = source;
    const { group_index: to_index, insert } = target;
    if (from_index === to_index && insert !== "after") return;
    this.changeIndex(from_index, to_index, insert);
  };

  async addGroup() {
    const group = defaultGroup();
    this.data.unshift(group);
    this.open_state[group.id] = true;
    this.markDataRevision();
    if (this.searchActive) {
      this.forceVisibleGroup(group.id);
    }
    await this.addRuleToGroup(0, defaultRule(), false);
    await tick();
    const el = document.querySelector(`.group-header[data-group-index="0"]`);
    el?.querySelector<HTMLInputElement>("input.group-name")?.focus();
  }

  deleteGroup = (index: number, confirmed = false) => {
    if (!confirmed && !confirm(t("Delete this group?"))) return;
    const removed = this.data[index];
    this.data.splice(index, 1);
    if (removed) {
      this.removeForcedGroup(removed.id);
      delete this.open_state[removed.id];
    }
    this.markDataRevision();
  };

  cloneGroupWithNewIds(group: Group): Group {
    return cloneGroupWithNewIdsData(group);
  }

  sortGroupRules(groupIndex: number, field: SortField, direction: SortDirection) {
    const group = this.data[groupIndex];
    if (!group) return false;
    sortGroupRulesData(group, field, direction);
    this.markDataRevision();
    return true;
  }

  restoreGroupRulesOrder(groupIndex: number, ruleIds: string[]) {
    const group = this.data[groupIndex];
    if (!group) return false;

    const restored = restoreGroupRulesOrderData(group, ruleIds);
    if (!restored) return false;
    this.markDataRevision();
    return true;
  }

  toConfigPayload() {
    return toConfigPayloadData($state.snapshot(this.data));
  }

  async cloneGroupsWithNewIds(groups: Group[]) {
    return cloneGroupsWithNewIdsData(
      groups,
      () => this.#yieldToMain(),
      IMPORT_GROUPS_CLONE_CHUNK_SIZE,
    );
  }

  async addGroups(groups: Group[]) {
    if (!groups.length) return;
    await prependGroupsData(
      this.data,
      this.open_state,
      groups,
      () => this.#yieldToMain(),
      IMPORT_GROUPS_INSERT_CHUNK_SIZE,
    );
    this.markDataRevision();
  }

  async overwriteGroups(groups: Group[]) {
    this.finishedCount = 0;
    this.renderLimit = 1;
    this.data.splice(0, this.data.length);
    this.open_state = {};
    await this.addGroups(groups);
    if (!groups.length) {
      this.markDataRevision();
    }
  }

  async addRulesToGroup(groupIndex: number, rules: Rule[]) {
    const group = this.data[groupIndex];
    if (!group || !rules.length) return;

    await prependRulesData(group, rules, () => this.#yieldToMain(), IMPORT_RULES_CHUNK_SIZE);
    this.markDataRevision();
  }

  async #yieldToMain() {
    if (typeof window === "undefined") return;
    await new Promise<void>((resolve) => {
      window.setTimeout(resolve, 0);
    });
  }
}
