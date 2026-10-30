# rasputin-app-catalog — agent instructions

The curated first-party app catalog for [Rasputin](https://rasputin.geekdojo.com) — the set of tiles an owner installs *from*.

## Engineering standard

This repo follows the [Geekdojo development principles](https://github.com/geekdojo/geekdojo-brain/blob/main/engineering/development-principles.md), the architecture and coding standard for every Geekdojo product (Decided 2026-09-28). Read it before you plan or write code. Plans and code are reviewed against it, and review findings cite its rule IDs (for example `ARCH-IOC`).

- **It applies to all new code.** Legacy code is refactored toward it when a change can reasonably do so.
- **A departure needs an approved exception.** The process is in [Standard exceptions](https://github.com/geekdojo/geekdojo-brain/blob/main/engineering/standard-exceptions.md). Approved exceptions for this repo are listed in `EXCEPTIONS.md` at the repo root. The file is created when the first one is approved.

## Tile secrets

Any tile that needs a password, key or token follows [`docs/tile-secrets.md`](docs/tile-secrets.md). It is binding: the platform generates the value from a `${secret:<name>}` token in `environment:`, the tile lists `tile.secrets` in `requires`, and a tile never ships a literal value or generates a secret inside the app. A value only the owner has, such as their own API key, is an operator-supplied secret: the tile declares it under `operatorSecrets` in `tile.json`, uses it as the same token, and also lists `tile.operator-secrets` (R6). No tile may use that capability until the stable control-plane release R6 names. tilelint links the guide from every secret refusal.
