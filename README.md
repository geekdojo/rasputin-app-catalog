# rasputin-app-catalog

The curated first-party app catalog for [Rasputin](https://rasputin.geekdojo.com) — the set of
tiles an owner installs *from*. One directory per tile, holding the metadata a cluster needs and
the compose stack it runs.

This repo publishes on its own cadence. **A new app does not require a Rasputin release.**

## Why this repo exists

The catalog used to be `go:embed`-ed into the control-plane binary. That meant adding an app cost
a lockstep version cut across three repos against a two-hour build floor — for content that
changes daily. Worse, it taught the operator that *new apps require a firmware upgrade*, which is
a NAS-vendor experience rather than a homelab one.

The full reasoning, and the contract that decoupling creates, is
**ADR-0006: App catalog delivery** (internal wiki).

## Layout

```
tiles/<id>/
  tile.json           # metadata: collection, arch, ports, RAM floor, exposure, status
  docker-compose.yml  # the stack (available tiles only; preview tiles are metadata-only)
```

`<id>` is the tile id and it is load-bearing: it is validated as a DNS-1123 label because it
appears verbatim in `<app>.<cluster-id>.internal`. **Renaming a tile id breaks the URL of an app
somebody has already installed.** Treat ids as frozen once published.

## Tile status

| Status | Means |
|---|---|
| `available` | Pinned and installable. Intended to mean "benched on real Pi 5 + N100 hardware" — see the exception below. |
| `preview` | Shown in the grid so the catalog reflects the roadmap; install is refused (409). May omit its compose entirely. |

A tile flips `preview` → `available` only after it clears the hardware bench: installed and
exercised on real Pi 5 and N100 nodes by a person. Desk research does not qualify.

> **Bench run, 2026-08-23/24.** Catalog v10 flipped thirteen tiles to `available` ahead of the
> bench, on Bryce's call, so the bench was owed for all of them. It has now run: every tile in v10
> was installed on real hardware — N100 and Pi 5 compute nodes on cluster `e3bench` — and its LAN
> URL fetched and checked for that app's expected first page. **Fifteen of eighteen passed.**
> `paperless-ngx` never came up and `pi-hole` failed mid image-pull; both were pulled from the
> catalog in v11 rather than left broken in front of an owner. `home-assistant` deployed cleanly
> but answered `400 Bad Request`, because it rejects a proxied request unless it is told to trust
> the proxy; its tile now seeds that configuration.
>
> **v11 re-bench, 2026-08-24: Home Assistant passes.** It deploys on an arm64 node and its LAN URL
> serves the onboarding screen rather than a `400`. Getting there took a control-plane fix as well
> as the tile one: the seed is a one-shot container, and the agent read a container that exits by
> design as a crash, so the deploy failed outright until `rasputin-control-plane#185` taught it to
> read the exit code alongside the state. **All sixteen tiles in v11 have now cleared the bench.**
>
> **Correction, 2026-08-29: that Home Assistant pass was an artifact of checking too early.**
> Home Assistant treats a changed `http:` block as a five-minute *trial* and reverts it if no
> admin confirms it — and on a fresh install no account exists yet to confirm anything. So the
> tile served its onboarding screen for five minutes, then reverted to a config with no proxy
> trust in it and went back to `400` for good. Both earlier "passes" were checks that landed
> inside that window. v13 seeds the setting where Home Assistant now keeps it and re-benched
> with a check at T+19s **and** T+9m54s.
>
> **Validated through the published catalog, 2026-08-29.** That v13 re-bench ran from a
> hand-applied compose, because an unmerged tile cannot reach a cluster — so it proved the fix and
> not the delivery. Repeated from the signed release: `e3bench` fetched catalog v13, and the
> compose that ran on an arm64 node hashes byte-identical to the one inside the signed
> `catalog.json` asset (`2069bc0e…`), which itself matches the published artifact (`e4c9c1fe…`).
> The app served its onboarding screen at **T+32s and again at T+8m51s**, past the five-minute
> trial window, with the setting in Home Assistant's `stable` store, nothing pending, no revert in
> its log and zero container restarts. **All sixteen tiles have now cleared the bench**, and this
> one cleared it the way an owner would actually install it.

**`ramFloorMB` today is upstream's documented minimum, cited — not a measured figure.** The bench
proves a tile boots and serves its first page; it does not measure the tile's memory. Read the
badge as the vendor's floor, and not as a number we have observed.

Today: **16 tiles — 16 available, 0 preview, all sixteen bench-passed.** Every tile has been
installed on real hardware and had its URL checked for the app's expected first page.

## Catalog version

The repo root holds a `VERSION` file containing a single positive integer:

```
1
```

That number is the **catalog version**, and it goes inside the signed bundle. A cluster refuses any
bundle whose version does not exceed the one it already holds ([ADR-0006 Decision 5]). This closes
an attack that needs no forged signature: replay a genuinely-signed *older* bundle and the fleet
rolls back to image digests with known CVEs. A signature check cannot tell that from a real update,
so the version gate has to.

**If your pull request changes anything under `tiles/`, raise `VERSION` by one.** CI enforces it —
the `version` job compares your branch against the base and fails if the corpus moved and the number
did not. A pull request that touches only docs or workflows needs no bump, and the job skips itself.

Two consequences worth knowing before they surprise you:

- **Forward only.** A bad publish is fixed by publishing a *higher* version, never by re-releasing a
  previous one. There is no rollback.
- **Two open pull requests can pick the same number.** Whichever merges second still says the same
  value, and the publish job refuses to reuse a version that is already released. Bump again and
  push; nothing is broken, but the second author is the one who has to do it.

Building the bundle by hand, if you want to see what gets published:

```bash
go run ./cmd/catalogbundle -version 1 -o catalog.json
```

`-version` is required and has no default — there is no safe guess for a monotonic counter.
Add `-o -` to write to your terminal instead of a file.

[ADR-0006 Decision 5]: https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/adr/0006-app-catalog-delivery.md

## Validation

The tile contract lives in one place, the `tileschema` Go module in `rasputin-control-plane`, and
both sides import it: this repo validates a tile before publishing, and the control plane
validates it again before loading it into a running cluster. There is deliberately **one
implementation** — two hand-maintained lists drift, and the drift is silent because each side
stays internally green.

Two halves:

- **`ValidateTile`** — authored metadata. Everything expressible in `tile.json`.
- **`ValidateTileSafety`** — the security-relevant properties of the compose stack: images must be
  digest-pinned (a tag is mutable at the registry, however specific it looks), host devices only
  when the tile declares `needsHardware`, and — since ADR-0006 Decision 12 — every other privilege
  **declared rather than refused**.

### Privilege: declared and consented, not refused

The catalog used to refuse `privileged`, host networking, host PID/IPC, any `cap_add` and any bind
mount outside two roots. That blocked apps people actually want (Home Assistant needs three of the
five) while saying nothing about `security_opt: seccomp=unconfined`, which is the closest a stack
gets to privileged without spelling it that way.

The rule now is **no *undeclared* privilege**. A tile states what its stack takes; the validator
derives the same thing from the compose and refuses the tile when the compose takes **more**.
Under-declaration is the error — over-declaring is allowed, so a tile can be conservative.

```json
"requires": ["privilege-tiers-v1"],
"privilege": {
  "tier": "host-trusting",
  "grants": ["privileged", "host-network", "device:/dev/ttyUSB0"],
  "why": "controls the smart devices on your network, and talks to USB radios"
}
```

Three tiers — `routine` (nothing outside its own container), `elevated` (reaches past itself but is
not root-equivalent) and `host-trusting` (effectively root on the node). The container runtime
socket is flagged separately from the tier, because it is not merely root-equivalent: it is the
ability to escape any constraint added later.

**Do not hand-write the grants.** `tilelint` derives them and prints the exact block to paste.

**One thing stays an absolute refusal**, and no tier permits it: a path reaching the platform's own
trust chain — `/etc/rasputin`, `/var/lib/rasputin` and anything containing them, `/var/lib/rasputin/apps/`
excepted. Not because it is the riskiest thing, but because consent is only meaningful while the
thing asking for it is still trustworthy; an app that can rewrite the trust store authorises every
future update.

Those safety facts are **derived here at publish time** and carried in the signed bundle manifest.
The control plane never parses compose — it validates the derived facts, which the bundle
signature makes exactly as trustworthy as the compose they came from.

### Volumes: every one classified, no default

Every named volume a tile's stack creates must say two things about itself, and there is **no
default for either** — an absent field is a refusal, not an inference (design `storage.md` §4.2).

```json
"volumes": [
  { "name": "vaultwarden-data", "backup": "critical", "quiesce": "stop" },
  { "name": "jellyfin-cache",   "backup": "cache",    "quiesce": "none" },
  { "name": "jellyfin-media",   "backup": "bulk",     "quiesce": "none" }
]
```

`backup` is what losing the volume costs:

| Class | What it holds | Backed up |
|---|---|---|
| `critical` | Secrets, credentials, keys — data whose *staleness* is itself harmful | Always; the owner cannot turn it off |
| `state` | Irreplaceable app state | Always by default; the owner may exclude it |
| `cache` | A regenerable index, queue or model cache | **Never** |
| `bulk` | A user media library, potentially terabytes | Opt-in per app |

`quiesce` is what it takes to copy it consistently: `none` (a plain copy is safe), `stop` (stop the
service, copy, restart), or the engine-aware dumps `sqlite`, `postgres` and `mysql`.

Two rules of thumb, both worth more than they look:

- **`stop` is the normal answer and a dump is the exception.** Stopping the container gives
  clean-shutdown consistency for free, which is a *stronger* guarantee than a dump and needs the
  agent to know nothing about the engine. For a weekly 3 a.m. job on a home appliance a brief
  outage costs nothing. Reach for `sqlite`/`postgres`/`mysql` only when downtime is genuinely
  harmful — an app whose outage breaks something else in the house — and not merely because the
  volume happens to hold a database.
- **When torn between `cache` and `state`, choose `state`.** A `cache` volume is never copied at
  all, so a volume misfiled as one is a volume nobody discovers was missing until a restore. An
  over-large archive is recoverable; a missing one is not.

The one cross-field rule: a `cache` volume must declare `quiesce: none`, because a strategy on a
volume that is never copied describes work that never runs.

`tilelint` fails on any volume the compose creates that `tile.json` does not classify, and prints
the line to paste. It checks the reverse too — a classification naming a volume the stack no longer
creates is dead metadata that reads like coverage. `catalogbundle` enforces the same rule on the
publish path, so a corpus that cannot be linted cannot be signed either.

## Linting a tile

```
go run ./cmd/tilelint           # validate every tile, offline
go run ./cmd/tilelint -arch     # also ask the registries what each image publishes
go run ./cmd/tilelint -tile jellyfin
```

The offline run gates every PR. The `-arch` probe runs weekly and on `main` rather than on
PRs — it talks to Docker Hub and ghcr, and a contributor cannot fix a registry outage.

**Images must be digest-pinned.** A tag is mutable at the registry however specific it looks,
so `jellyfin/jellyfin:10.9.11` does not describe a fixed stack. Keep the tag for readability
and add the digest — `jellyfin/jellyfin:10.9.11@sha256:…` — which is the form the linter wants.

## Requesting an app

Open an issue with the **Request an app** template. A bot checks it is complete, that the
id is usable as a hostname, that the tag is pinned rather than floating, and that the
registry really publishes the architectures the request claims — asking the registry
rather than believing the tag.

Passing that is not acceptance. It means the request is worth a human reading. The catalog
is curated rather than comprehensive, so a complete, technically sound request can still be
declined because it does not fit the set.

A single-architecture app is fine. Rasputin runs arm64 (Pi 5) and amd64 (N100) nodes; a
tile declares what it supports and install offers only the nodes that can run it.

## How a request becomes a tile

```
issue  ──▶  triage bot  ──▶  agent drafts a PR  ──▶  gates  ──▶  human merges  ──▶  bench
            complete?        digest-pinned tile      tiles         curation        preview
            arch real?       + its evidence          supply-chain   judgement      → available
```

The agent stage is **manually dispatched**, not automatic: a human names the issue number,
which means a human has read it. Nothing starts an agent because a stranger opened an issue.

Every stage after the agent is deterministic code the agent cannot edit. It proposes a tile
and the evidence behind it; the gates decide whether that proposal is safe, and a person
decides whether it belongs. The last step is hardware — a tile ships as `preview` until it
clears the bench, and no part of this pipeline can grant `available`.

## Not here yet

This repo is being stood up incrementally. Still to land:

- the hardware bench itself, for 13 of the 18 tiles — which are already `available`, so this is
  a debt against shipped tiles rather than a gate in front of them. It is a person's job and stays
  that way; the E19 pipeline will not be automating it. Proving a published arm64 image really
  executes on a Pi is the one piece worth automating later; that is deferred, not dropped.
- the four `dongle` tiles were removed rather than authored: ADS-B Ultrafeeder, AIS-catcher,
  rtl_433 and WeeWX all need a USB SDR passed into a container, and that passthrough design is
  still an open question. They come back when it is answered.
- Trilium Notes was authored and then pulled: 96 fixable HIGH/CRITICAL in the app's own
  dependencies, including a CRITICAL, is not a supply chain we want to hand somebody. It comes
  back if upstream clears them.

The signed bundle and its publish pipeline are live: **every merge to `main` publishes a new
signed catalog release.** Bundles are signed with a dedicated app-catalog leaf under Geekdojo's
IANA PEN 66587, carrying a catalog-only EKU — that leaf cannot sign an OS or firmware artifact
even though it shares the trust root. The control plane fetches and verifies that bundle on a
24-hour poll and refuses anything older than the catalog it already holds. The app-request intake
template and the agent-assisted drafting pipeline have landed too — see **Requesting an app** above.

Until the publish pipeline exists, `rasputin-control-plane` keeps its own copy of `tiles/` as the
shipping catalog. **This repo is the authoring home; that copy is a temporary duplicate** and is
removed once the control plane pins a published catalog release.

## License

AGPL-3.0, matching the rest of the Rasputin source.
