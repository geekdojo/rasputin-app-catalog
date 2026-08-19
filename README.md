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

A tile flips `preview` → `available` only after it clears the hardware bench — real RAM/CPU
measured, the published arm64 image proven to actually run on the Pi, and a screenshot captured.
Desk research does not qualify.

Today: **23 tiles — 5 available, 18 preview.**

## Validation

The tile contract lives in one place, the `tileschema` Go module in `rasputin-control-plane`, and
both sides import it: this repo validates a tile before publishing, and the control plane
validates it again before loading it into a running cluster. There is deliberately **one
implementation** — two hand-maintained lists drift, and the drift is silent because each side
stays internally green.

Two halves:

- **`ValidateTile`** — authored metadata. Everything expressible in `tile.json`.
- **`ValidateTileSafety`** — the security-relevant properties of the compose stack: images must be
  digest-pinned (a tag is mutable at the registry, however specific it looks), no privileged
  containers, no host networking, no host PID/IPC, no added capabilities, bind mounts confined to
  allowed roots, and host devices only when the tile declares `needsHardware`.

Those safety facts are **derived here at publish time** and carried in the signed bundle manifest.
The control plane never parses compose — it validates the derived facts, which the bundle
signature makes exactly as trustworthy as the compose they came from.

## Not here yet

This repo is being stood up incrementally. Still to land:

- the publish-time linter and CI wiring,
- the signed, versioned bundle and its publish pipeline,
- the app-request intake template and the agent-assisted drafting pipeline,
- the hardware bench stage that flips `preview` → `available`.

Until the publish pipeline exists, `rasputin-control-plane` keeps its own copy of `tiles/` as the
shipping catalog. **This repo is the authoring home; that copy is a temporary duplicate** and is
removed once the control plane pins a published catalog release.

## Licence

AGPL-3.0, matching the rest of the Rasputin source.
