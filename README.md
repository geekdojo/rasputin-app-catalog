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
| `available` | Benched on real Pi 5 + N100 hardware, pinned, installable. |
| `preview` | Shown in the grid so the catalog reflects the roadmap; install is refused (409). May omit its compose entirely. |

A tile flips `preview` → `available` only after it clears the hardware bench: installed and
exercised on real Pi 5 and N100 nodes by a person. Desk research does not qualify.

**`ramFloorMB` today is upstream's documented minimum, cited — not a measured figure.** That is
true of every tile in the corpus, including the `available` ones: the bench converts desk figures
into measured ones and has not yet run for them. Read the badge as the vendor's floor, and not as
a number we have observed.

Today: **23 tiles — 5 available, 18 preview.**

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

## Linting a tile

```
go run ./cmd/tilelint           # validate every tile, offline
go run ./cmd/tilelint -arch     # also ask the registries what each image publishes
go run ./cmd/tilelint -tile pi-hole
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

- the hardware bench itself, for 18 of the 23 tiles. It is a person's job and stays that way —
  the E19 pipeline will not be automating it. Proving a published arm64 image really executes on
  a Pi is the one piece worth automating later; that is deferred, not dropped.

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
