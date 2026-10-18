# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

WeirdTrickle is a fork of MagiTrickle, a DNS-based traffic routing utility for routers (Entware/OpenWrt). It intercepts DNS queries via a MITM proxy, caches resolved IPs, matches them against domain rules, and programs iptables/ipset to route matched traffic through a specified network interface.

The package name `magitrickle`, service `S99magitrickle`, binary `magitrickled`, Go module `magitrickle` and configuration paths intentionally retain their original names so existing installations can upgrade without moving their settings. User-facing names and fork links use WeirdTrickle.

## Build System

The project uses a Makefile with stamp-based incremental builds. Before building, copy and configure `.config.example` to `.config`:

```sh
cp .config.example .config
# Edit PLATFORM, TARGET, GOOS, GOARCH etc.
```

Key make targets:
```sh
make all              # download deps + build + package
make build            # build backend and frontend only
make build_backend    # Go binary only
make build_frontend   # Svelte frontend only
make package          # create .ipk / .apk packages
make swagger          # regenerate docs/swagger.yaml with pinned Swag v1.16.6
make clean            # remove all build artifacts
make clear            # remove build dir for current PLATFORM/TARGET + frontend dist
```

Build outputs land in `.build/<PLATFORM>_<TARGET>/`. The final packages appear in `.build/`.

Two supported platforms (set via `PLATFORM` in `.config`):
- `entware` — Keenetic and similar; targets like `mipsel-3.4`, `aarch64-3.10`. The `_kn` suffix targets add `entware_kn` build tag and require `socat`.
- `openwrt` — targets like `aarch64_cortex-a53`.

Backend binary is compressed with UPX during build (skipped for riscv64, mips64, mips64le, loong64).

## Backend

**Entry point**: `src/backend/cmd/magitrickled/main.go`  
**Go module**: `magitrickle` (Go 1.26.9; see `go.mod`)

Run tests:
```sh
cd src/backend
go test -race -tags testing ./...  # unit/API/firewall regression tests on Linux
go vet -tags testing ./...
go test -tags testing ./models/... # single package
```

### Architecture

The root package (`src/backend/*.go`) contains the `App` struct — the central coordinator:

- **`app.go`** — `App` struct and CRUD methods for groups and subscriptions
- **`config.go`** — validates YAML and schema compatibility before applying it; failed loads prevent startup and saving over the original file; saves use a temporary file and atomic rename
- **`start.go`** — `App.Start()`: wires up DNS MITM proxy, iptables helper, HTTP/Unix API servers, and the netlink watcher
- **`dns.go`**, **`dns_snapshot.go`** — DNS hooks and a conservative prefilter; matching addresses must be routed before returning a usable answer, including pending subscription rules without `Spec.Model`
- **`rule_set.go`** — `RuleSet` wraps a group spec with ipset + iptables lifecycle (Enable/Disable/Sync)
- **`subscriptions.go`** — subscription-level rule set management and auto-update scheduling

Key sub-packages:

| Package | Purpose |
|---|---|
| `app/` | `Main` and `RuleSet` interfaces (used for testing and API layer) |
| `api/` | HTTP server (chi, port 8080) and Unix socket; mounts `api/v1` |
| `models/` | Data types: `Group`, `Rule`, `Subscription`, `AppConfig`; `Group`, `Rule`, `Subscription` carry YAML struct tags and custom `UnmarshalYAML` (absent `enable` defaults to `true`) |
| `config/` | YAML-serializable structs for app-level settings only (`App`, `HTTPWeb`, `DNSProxy`, etc.); groups and subscriptions are stored directly as `models.Group`/`models.Subscription` — no separate config types for them |
| `constant/` | Default config values; platform-conditional paths and ignored interfaces via build tags (`entware`, `entware_kn`, `openwrt`) |
| `groups/` | Builds `rulesets.Spec` from a `models.Group` for user-defined groups |
| `subscriptions/` | Fetches, parses, and validates external domain lists; builds subscription rule sets |
| `rulesets/` | `Spec` type — common data carrier for both user groups and subscription groups |
| `utils/dnsMITMProxy/` | UDP/TCP DNS proxy with request/response hooks |
| `utils/netfilterTools/` | iptables chain management, ipset CRUD, port remap (53→3553) |
| `utils/recordsCache/` | In-memory DNS A/AAAA/CNAME cache with TTL cleanup |
| `utils/intID/` | 4-byte ID type used for groups and rules |
| `utils/iptables/` | Low-level iptables wrapper with batched commit |

### DNS flow

1. iptables `nat PREROUTING` redirects port 53 → 3553 (configurable, can be disabled)
2. `dnsMITMProxy` forwards queries to upstream (default `127.0.0.1:53`) and intercepts responses
3. `dnsResponseHook` calls `handleMessage`, which processes A/AAAA/CNAME and HTTPS/SVCB bindings and address hints
4. Matching IPs are added to the group's ipset with TTL = DNS TTL + `AdditionalTTL` (default 3600s)
5. iptables routes packets from the ipset through the group's configured interface

Subscriptions are evaluated first, then Groups set the final routing priority. Preserve this ordering for IPv4/IPv6 and after updates or firewall reconstruction. Rule updates prepare replacement sets before publishing them; interface changes keep a blackhole route during the transition. DNS addresses that match a rule but cannot be installed produce SERVFAIL. These mechanisms do not cover client DNS that bypasses the proxy or traffic without a matching rule.

Subscription fetching has a separate shared interface setting, `app.subscriptionFetchInterface`. If that interface fails, fetching retries through the system route. This fallback is intentional and does not change the traffic-routing interface selected inside each group/subscription.

### Rule types

Defined in `models/rule.go`: `domain` (exact), `namespace` (domain + subdomains), `wildcard` (`*`/`?`), `regex` (dlclark/regexp2), `subnet` (IPv4 CIDR), `subnet6` (IPv6 CIDR).

## Frontend

**Location**: `src/frontend/`  
**Stack**: Svelte 5, TypeScript, Vite, Prettier

```sh
cd src/frontend
npm ci
npm run dev:frontend    # Vite dev server (needs separate backend or mock)
npm run dev:backend     # Deno mock backend (API mock for local UI dev)
npm run build           # production build → dist/
npm run check           # svelte-check + tsc type check
npm run format          # Prettier format
npm run format:check    # Prettier check
npm run test:e2e        # Playwright end-to-end tests
npm run test:unit       # Deno unit tests
```

For local development, run both `dev:backend` (Deno mock at `dev/backend-mock.ts`) and `dev:frontend` in separate terminals.

Built frontend is placed into the package at `usr/share/magitrickle/skins/default/` and served by the HTTP server. The active skin is set by `HTTPWeb.Skin` config (default: `"default"`).

## Configuration

Runtime config is YAML; platform-specific default paths are in `constant/path_*.go`. Default values are in `constant/constant.go`:
- DNS proxy listens on `[::]`:3553, upstream `127.0.0.1:53`
- HTTP WebUI at `[::]`:8080
- iptables chain prefix `MT_`, ipset prefix `mt_`
- Default monitored interface: `br0`

`constant.ConfigVersion` is the YAML schema version (`0.7.0`), separate from the release version. Keep compatibility with original `0.x` configs and calendar-version configs from early fork releases. Do not set `configVersion` to the number in `VERSION`.

On Entware, settings are in `/opt/var/lib/magitrickle/config.yaml`. The installer backs up existing settings/state under `/opt/var/backups/magitrickle/` before replacing the package. Updates use `weirdtrickle-update`; `magitrickle-update` is a compatibility alias. Calendar releases require one invocation of the current installer to migrate to three-part versions.

## CI

GitHub Actions (`.github/workflows/build.yml`) checks `develop`, `codex/**`, pull requests and `v*` tags. Tests include Go/race/vet, generated Swagger consistency, vulnerability scans, real IPv4/IPv6 routing in isolated Linux network namespaces, installer checks, and frontend unit/browser tests. The separate `check.yml` also checks pull requests.

Automatic builds produce the seven Entware configurations under `config/entware/`. OpenWrt is optional: run `workflow_dispatch` with `platforms: all` to build `config/*/*.config`. These CI checks do not establish acceptance on a physical Keenetic/Netcraze router. Never run the namespace firewall tests against a live router's main network namespace.

## Versions and releases

`VERSION` is the source of the `MAJOR.MINOR.PATCH` release number used in the backend, WebUI and package; `-1` is the package revision. Local and branch builds use the same number without an automatic dev marker. The updater cannot distinguish such builds from a published release with the same number and package identity.

For a release, update `VERSION`, add a dated `## [X.Y.Z]` section to `CHANGELOG.md`, validate and merge the changes into `develop`, then push the matching unused tag `vX.Y.Z`. `scripts/release_version.py` rejects invalid/mismatched tags and missing, empty or duplicate release-note sections. `--notes` extracts that release's notes. Only tags publish releases, after all tests and packages succeed; existing published assets are immutable. Ordinary branch/PR builds do not publish releases.

## API documentation and support

API annotations are in `src/backend/api/`; the general metadata is in `api/doc.go`. `make swagger` regenerates `docs/swagger.yaml` from them without changing runtime dependencies. CI regenerates it and rejects a diff. API version `1.0` denotes the v1 contract, not the application release or config schema.

Report fork issues at https://github.com/WeirdPhrog/WeirdTrickle/issues. Keep upstream MagiTrickle links clearly labelled as the original project in README and the visible About dialog.
