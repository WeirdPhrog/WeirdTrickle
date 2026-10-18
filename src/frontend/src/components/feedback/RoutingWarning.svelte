<script lang="ts">
  import { onMount } from "svelte";

  import { token } from "../../data/auth.svelte";
  import { t } from "../../data/locale.svelte";

  import { API_BASE } from "../../utils/fetcher";

  type SuspendedRouting = {
    id: string;
    name: string;
    subscription: boolean;
    error: string;
    nextRetryAt: number;
  };

  let suspended = $state<SuspendedRouting[]>([]);
  let unavailable = $state(false);

  onMount(() => {
    let stopped = false;
    let inFlight: AbortController | undefined;

    async function refresh() {
      if (stopped || document.hidden || inFlight) return;
      const request = new AbortController();
      inFlight = request;
      const timeout = window.setTimeout(() => request.abort(), 8000);
      try {
        // A background status check must not produce a toast every time the
        // daemon is temporarily unavailable. Retain the last known warning.
        const response = await fetch(`${API_BASE}/system/routing`, {
          cache: "no-store",
          headers: token.current ? { Authorization: `Bearer ${token.current}` } : {},
          signal: request.signal,
        });
        if (response.status === 401 || response.status === 403) token.reset();
        if (!response.ok) throw new Error("Routing status unavailable");
        const body = await response.json();
        if (
          !Array.isArray(body.suspended) ||
          body.suspended.some(
            (item: SuspendedRouting) =>
              !item ||
              typeof item.id !== "string" ||
              typeof item.name !== "string" ||
              typeof item.subscription !== "boolean" ||
              typeof item.error !== "string" ||
              !Number.isFinite(item.nextRetryAt),
          )
        )
          throw new Error("Invalid routing status");
        if (!stopped) {
          suspended = body.suspended;
          unavailable = false;
        }
      } catch {
        if (!stopped) unavailable = true;
      } finally {
        window.clearTimeout(timeout);
        if (inFlight === request) inFlight = undefined;
      }
    }

    const resume = () => void refresh();
    resume();
    const timer = window.setInterval(resume, 30_000);
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("focus", resume);
    return () => {
      stopped = true;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("focus", resume);
      inFlight?.abort();
    };
  });
</script>

{#if suspended.length > 0}
  <section class="routing-warning" role="status" aria-live="polite">
    <h2>{t("Routing suspended")}</h2>
    <p>{t("Routing recovery is pending; traffic may be blocked.")}</p>
    {#each suspended as item (`${item.subscription}:${item.id}`)}
      <div class="suspended-set">
        <strong>{t(item.subscription ? "Subscriptions" : "Groups")}: {item.name || item.id}</strong>
        <span class="recovery-error">{item.error}</span>
        <span
          >{t("Retry no earlier than")}: {new Date(item.nextRetryAt * 1000).toLocaleString()}</span
        >
      </div>
    {/each}
    {#if unavailable}
      <p>{t("Routing status unavailable; showing the last known state.")}</p>
    {/if}
  </section>
{/if}

<style>
  .routing-warning {
    margin: 0.75rem 0;
    padding: 0.8rem 1rem;
    border: 1px solid var(--yellow-bright);
    border-radius: 8px;
    background: var(--bg-light);
    color: var(--text);
    overflow-wrap: anywhere;
  }

  h2 {
    margin: 0;
    font-size: 1rem;
    color: var(--yellow-bright);
  }

  p {
    margin: 0.4rem 0;
    font-size: 0.85rem;
  }

  .suspended-set {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    margin-top: 0.75rem;
    font-size: 0.85rem;
  }

  .recovery-error {
    white-space: pre-wrap;
  }
</style>
