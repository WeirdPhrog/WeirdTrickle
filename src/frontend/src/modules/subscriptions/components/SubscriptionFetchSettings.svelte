<script lang="ts">
  import { onMount } from "svelte";

  import Select from "../../../components/ui/Select.svelte";
  import { DIRECT_INTERFACE, interfaces } from "../../../data/interfaces.svelte";
  import { t } from "../../../data/locale.svelte";

  import { toast } from "../../../utils/events";
  import { fetcher } from "../../../utils/fetcher";

  type Settings = { fetchInterface: string };
  let selected = $state("");
  let saved = "";
  let loaded = $state(false);
  let saving = $state(false);
  let loadFailed = $state(false);
  let options = $derived([
    { value: "", label: t("System default") },
    ...interfaces.list
      .filter((item) => item.id !== "blackhole" && item.id !== DIRECT_INTERFACE)
      .map((item) => ({ value: item.id, label: item.id, description: item.name })),
  ]);

  async function load() {
    loadFailed = false;
    try {
      const settings = await fetcher.get<Settings>("/subscriptions/settings");
      selected = saved = settings.fetchInterface;
      loaded = true;
    } catch {
      loadFailed = true;
    }
  }

  async function save(value: string) {
    if (!loaded || saving || value === saved) return;
    saving = true;
    try {
      const settings = await fetcher.put<Settings>("/subscriptions/settings", {
        fetchInterface: value,
      });
      selected = saved = settings.fetchInterface;
      toast.success(t("Saved"));
    } catch {
      selected = saved;
    } finally {
      saving = false;
    }
  }

  onMount(() => {
    void load();
  });
</script>

<div class="subscription-fetch-settings">
  <div class="setting-control">
    <span>{t("Download interface")}</span>
    <Select
      {options}
      bind:selected
      onValueChange={save}
      disabled={!loaded || saving}
      ariaLabel={t("Download interface")}
    />
    {#if saving}<span class="hint" role="status">{t("saving changes...")}</span>{/if}
    {#if loadFailed}<button onclick={load}>{t("Retry")}</button>{/if}
  </div>
  <p class="hint">
    {t(
      "For all subscriptions. Saves automatically. If unavailable, retries via the system default route.",
    )}
  </p>
</div>

<style>
  .subscription-fetch-settings {
    margin: 1rem 0;
    padding: 0.75rem 1rem;
    border: 1px solid var(--bg-light-extra);
    border-radius: 0.5rem;
  }
  .setting-control {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .hint {
    color: var(--text-2);
    font-size: 0.85rem;
  }
  p {
    margin: 0.4rem 0 0;
  }
</style>
