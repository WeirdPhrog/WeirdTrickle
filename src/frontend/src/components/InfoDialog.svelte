<script lang="ts">
  import { t } from "../data/locale.svelte";
  import GenericDialog from "./ui/GenericDialog.svelte";
  import GenericIcon from "./ui/GenericIcon.svelte";

  import { Bug, Github, Gitlab, Globe, Sponsorship } from "./ui/icons";

  const version = import.meta.env.VITE_PKG_VERSION || "0.0.0";

  type Props = {
    open: boolean;
  };

  let { open = $bindable() }: Props = $props();
</script>

<GenericDialog {open} title={t("About")} on:close={() => (open = false)} maxWidth={420}>
  <div slot="body" class="about-body">
    <div class="container">
      <div class="block">
        <div class="icon">
          <Github size={22} />
        </div>
        <div class="text">
          {t("WeirdTrickle on GitHub")}
          <a
            target="_blank"
            rel="noopener noreferrer"
            href="https://github.com/WeirdPhrog/WeirdTrickle"
            >https://github.com/WeirdPhrog/WeirdTrickle</a
          >
        </div>
      </div>
      <div class="block">
        <div class="icon">
          <Bug size={22} />
        </div>
        <div class="text">
          {t("Report a WeirdTrickle issue")}
          <a
            target="_blank"
            rel="noopener noreferrer"
            href="https://github.com/WeirdPhrog/WeirdTrickle/issues"
            >https://github.com/WeirdPhrog/WeirdTrickle/issues</a
          >
        </div>
      </div>
      <h3>{t("Based on the original MagiTrickle project")}</h3>
      <div class="block">
        <div class="icon">
          <Globe size={22} />
        </div>
        <div class="text">
          {t("Official website")}

          <a target="_blank" rel="noopener noreferrer" href="https://magitrickle.dev"
            >https://magitrickle.dev</a
          >
        </div>
      </div>
      <div class="block">
        <div class="icon">
          <Bug size={22} />
        </div>
        <div class="text">
          {t("MagiTrickle Bug Tracker")}
          <a
            target="_blank"
            rel="noopener noreferrer"
            href="https://gitlab.com/magitrickle/magitrickle/-/boards"
            >https://gitlab.com/magitrickle/magitrickle/-/boards</a
          >
        </div>
      </div>
      <div class="block">
        <div class="icon">
          <Gitlab size={22} />
        </div>
        <div class="text">
          {t("GitLab Repository")}
          <a
            target="_blank"
            rel="noopener noreferrer"
            href="https://gitlab.com/magitrickle/magitrickle"
            >https://gitlab.com/magitrickle/magitrickle</a
          >
        </div>
      </div>
      <div class="block">
        <div class="icon">
          <GenericIcon name="Telegram" size={22} x={1} y={1} />
        </div>
        <div class="text">
          {t("Telegram Chat")}
          <a target="_blank" rel="noopener noreferrer" href="https://t.me/MagiTrickleChat"
            >https://t.me/MagiTrickleChat</a
          >
        </div>
      </div>
      <div class="block">
        <div class="icon">
          <Sponsorship size={22} />
        </div>
        <div class="text">
          {t("Financial Contributions")}
          <a target="_blank" rel="noopener noreferrer" href="https://boosty.to/magitrickle">
            https://boosty.to/magitrickle
          </a>
        </div>
      </div>
    </div>
    <div class="build">
      WeirdTrickle · {t("Build")}: {version}
    </div>
  </div>
</GenericDialog>

<style>
  .about-body {
    max-height: calc(100vh - 8rem);
    max-height: calc(100dvh - 8rem);
    overflow-y: auto;
  }

  a {
    display: block;
    font-size: smaller;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
    flex: 1;
    color: var(--accent);
  }

  a:hover {
    color: var(--text);
  }

  .container {
    display: flex;
    flex-direction: column;
    gap: 0.8rem;
    min-width: 0;
    flex: 1;
  }

  .block {
    display: flex;
    flex-direction: row;
    align-items: center;
    gap: 0.6rem;
  }

  h3 {
    margin: 0.4rem 0 0;
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--text-2);
  }

  .text {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .icon {
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--text-2);
  }

  .build {
    font-size: smaller;
    color: var(--text-2);
    width: 100%;
    text-align: right;
    margin-top: 1rem;
  }
</style>
