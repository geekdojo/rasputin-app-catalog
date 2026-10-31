# Secrets in a tile

This guide is for anyone writing or reviewing a tile that needs a password, a key or a token.
It tells you how the tile gets that value from the platform, what never to do instead, and how to
fix each refusal the linter prints. Everything you need to follow it is on this page.

Each rule ends with a **Source of record** line. That line names the Geekdojo decision the rule
comes from, so a maintainer can trace it. Those documents are Geekdojo-internal and may not be
public. You never need to open one to follow the rule: the rule, its reason and its fix are all
stated here.

## Terms

- **Tile**: one app in this catalog. It is a directory, `tiles/<id>/`, holding a `tile.json`
  (the app's metadata) and a `docker-compose.yml` (the containers it runs).
- **`<id>`**: the tile's directory name, which is also the `id` field in its `tile.json`. For
  example, `tiles/mealie/` has the id `mealie`.
- **Compose**: the tile's `docker-compose.yml` file, in the Docker Compose format. A *service*
  is one container in it; `environment:` is the list of environment variables a service gets.
- **Control plane**: the Rasputin software running on an owner's cluster. It reads this catalog,
  installs tiles and keeps their secrets.
- **Deploy**: the control plane starting, or restarting, a tile's containers on a node.
- **`requires` capability**: an entry in the `requires` list of `tile.json`. It names a feature
  of the tile format that the tile depends on.
- **Must-understand**: the rule that gives `requires` its teeth. A control plane refuses to
  install a tile that lists a capability it does not know, rather than installing it wrongly.
- **Derived secret**: a value the platform creates for the tile, such as a database password.
  Nobody types it. Every token is a derived secret unless the tile declares it operator-supplied.
- **Operator-supplied secret**: a value only the owner has, such as their own API key for a
  third-party service. The tile declares it in `tile.json`, under `operatorSecrets`, and the
  owner sets the value; the platform never creates it (rule R6).
- **`<name>`**: a secret's label, which you choose, such as `db-password`. Rule R1 says which
  characters it may contain.
- **Token**: the text `${secret:<name>}` you write in the compose where the value must go.
- **tilelint**: this repository's linter, run as `go run ./cmd/tilelint`. Every pull request
  must pass it.

## The rules

### R1: write each secret as `${secret:<name>}` in an `environment:` value

Wherever a service needs a secret, write the token as the value of an environment variable:

```yaml
environment:
  POSTGRES_PASSWORD: "${secret:db-password}"
```

- **`<name>`** uses lowercase `a`-`z`, digits `0`-`9` and hyphens (`-`), is 1 to 63 characters
  long, and does not start or end with a hyphen. `db-password` and `api-key-2` are valid;
  `DB_Pass` and `-key` are not.
- **What the app receives** is a 43-character random string in base64url (the letters `A`-`Z`
  and `a`-`z`, the digits, `-` and `_`), with no `=` padding. If the app has its own rules for a
  password, check that this string meets them.
- **The value stays the same** across redeploys, moves to another node and upgrades of the tile.
  A fresh install is a new app, and it gets a new value.
- **One name means one value everywhere in the tile.** The value depends only on the installed
  app and the name, never on which service reads it. So when two services must agree on a
  value, such as an app and its database sharing a password, both use the same token:
  `${secret:db-password}` in each. The worked example below does exactly this.
- **Never rename a secret in a tile that has been published.** A new name is a new value. Many
  databases, Postgres among them, read their password only once, when they first create their
  data directory. After the rename the app would present the new value, the database would still
  expect the old one, and every installed copy of the tile would lose access to its own data.
  If a name is wrong, keep it.
- **Never put the token in `command:` or `entrypoint:`.** A container's command line is visible
  to every local user on the node through `ps`, so a secret there is not secret. Use
  `environment:` only.

Source of record (Geekdojo-internal, may not be public):
[ADR-0006](https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/adr/0006-app-catalog-delivery.md)
Decision 11a and its 2026-10-18 amendment; S14,
[geekdojo/geekdojo-brain#707](https://github.com/geekdojo/geekdojo-brain/issues/707).

### R2: add `tile.secrets` to `requires`

Any tile whose compose contains a token lists the capability in `tile.json`:

```json
"requires": ["tile.secrets"]
```

Keep any capability the tile already lists; `requires` is a list, so add to it. Reason: a control
plane too old to know this capability then refuses the tile. Without that refusal it would
install the tile and hand the app the literal text `${secret:db-password}` as its password.
Every stable control plane from v2026.10.0 onwards knows `tile.secrets`. tilelint refuses a
tile that uses a token without it.

Source of record (Geekdojo-internal, may not be public):
[ADR-0006](https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/adr/0006-app-catalog-delivery.md)
Decisions 11d and 7.

### R3: there is no file form

The platform puts a secret only where its token sits in the compose, as an environment variable.
It never delivers a secret as a file. If the app you are packaging can only read a secret from a
file, stop and open an issue in this repository describing the app. Do not work around it, for
example with a script that writes the variable to a file.

Reason: a file protects nothing extra. On a node, only root can read a container's environment,
and only root can read a mounted secret file, so both have the same exposure, and one way is
simpler to get right than two.

Source of record (Geekdojo-internal, may not be public): S19,
[geekdojo/geekdojo-brain#717](https://github.com/geekdojo/geekdojo-brain/issues/717), revised
2026-10-12.

### R4: never ship a literal

Never write a secret's value into the compose or `tile.json`: no `change-me`, no default
password, no example key, and no empty string standing in for a value. Use a token (R1).

Reason: this repository is public, and every cluster installs the same files. A value written
here is the same on every cluster and readable by anyone. Reviewers enforce this rule today;
tilelint will refuse literals once
[geekdojo/geekdojo-brain#552](https://github.com/geekdojo/geekdojo-brain/issues/552) lands.

Source of record (Geekdojo-internal, may not be public): Auth decision 21,
[auth methodology](https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/design/security/auth-methodology.md)
§5.3.

### R5: never generate a secret inside the app

Do not add an init container, an entrypoint script or a one-shot service that creates a secret
and writes it into a volume. Declare the secret with a token instead (R1), and let the platform
create and keep it.

Reason: there is one flow for every app. The tile declares what it needs, and the platform creates
the value and keeps it. That is what lets backup, restore and rotation work the same way for every
app. The human-system tile generates its secrets inside the app today; it is moving to tokens under
[geekdojo/geekdojo-brain#606](https://github.com/geekdojo/geekdojo-brain/issues/606), and is not a
pattern to copy.

Source of record (Geekdojo-internal, may not be public): Auth decision 21,
[geekdojo/geekdojo-brain#573](https://github.com/geekdojo/geekdojo-brain/issues/573) and
[auth methodology](https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/design/security/auth-methodology.md)
§9 item 21. That decision's "the platform holds nothing" half was replaced by S14; the rule against
generating secrets in the app stands.

### R6: declare an operator-supplied secret in `tile.json`

An operator-supplied secret is a value only the owner has, such as their own API key for a
third-party service. The platform cannot create it, so the tile declares it, and the owner sets
it. Three things go together:

1. **The declaration.** In `tile.json`, list each one under `operatorSecrets` with a `name` and a
   `label`:

   ```json
   "operatorSecrets": [
     { "name": "ais-key", "label": "AISStream API key" }
   ]
   ```

   - `name` follows R1's rules for a secret name, and is declared once.
   - `label` is what the owner is shown when asked for the value. It may not be empty.
2. **The token.** The compose uses the name as an ordinary token in `environment:`, exactly as
   R1 describes: `AIS_API_KEY: "${secret:ais-key}"`. Every declared name must appear as a token
   in the compose; a declared name the compose never uses is refused, because the owner would be
   asked for a value that reaches no container. (A preview tile with no compose yet is exempt.)
3. **The capability.** `requires` lists both `tile.secrets` and `tile.operator-secrets`:

   ```json
   "requires": ["tile.secrets", "tile.operator-secrets"]
   ```

   Reason: a control plane that knows `tile.secrets` but not operator secrets would see an
   ordinary token, create a random value for it, and hand that to the app as the owner's key. With
   the capability listed, that control plane refuses the tile instead.

What the owner and the app see:

- **The deploy is refused until the owner sets every required declared value** the compose
  uses. The app is not started with an empty or made-up value; the refusal names each missing
  secret by its label, and the owner sets them and deploys again. An upgrade to a tile version that adds a
  declared secret also fails until the owner sets it; the containers already running are left
  as they were.
- **The value is write-only.** The owner can set or replace it, but nobody can read it back, not
  even the owner. The app receives it in its environment variable, as with any token.
- **A declared secret is required unless it says `required: false`.** Add `"required": false`
  to a declaration only when the app runs without the value, for example an optional second
  data feed. Until the owner sets an optional secret, the app receives an empty string in its
  environment variable. The deploy is not refused, and the platform never creates a value for
  the name. Leave the field out for a secret the app cannot run without: an absent `required`
  means required. A control plane older than the release that carries optional secrets
  (geekdojo-brain#604) ignores the field and treats the secret as required, refusing the deploy
  until the owner sets it, so the field never makes an older cluster hand the app a made-up value.

**Never turn a published derived name into an operator-supplied one.** If a tile has been
published with `${secret:<name>}` as a derived secret, every installed copy already holds a
value the platform created for that name. Declaring the same name operator-supplied would make
the platform hand that created value to the app as the owner's key. Choose a new name for the
operator-supplied secret instead. tilelint cannot see a tile's publish history, so reviewers
check this rule.

**Which clusters can run it.** Every control plane that knows `tile.operator-secrets` also has
the screen where an owner sets the declared values, so on those clusters the tile installs and
deploys once the owner has set them. A control plane older than the capability refuses this tile
alone, naming the capability it lacks, and loads the rest of the catalog. Reason: a cluster that
ignored the declaration would invent a value and hand it to the app as the owner's key. So a
catalog release can carry the tile before every cluster can run it; Geekdojo checks which tiles
stable clusters will refuse before promoting a release to them.

The worked example below adds an operator-supplied key to a single-service app.

`tiles/example-feeds/tile.json`:

<!-- tilelint-example: operator tile.json -->
```json
{
  "id": "example-feeds",
  "name": "Example Feeds",
  "tagline": "Live ship positions with your own API key.",
  "description": "A worked example for the secrets guide: an app that needs the owner's own key for a third-party data feed.",
  "collection": "show-off",
  "category": "data",
  "arch": "both",
  "ramFloorMB": 1024,
  "exposureDefault": "lan-only",
  "status": "preview",
  "ports": [
    {
      "name": "web",
      "container": 8000,
      "published": 8200,
      "protocol": "tcp",
      "web": true
    }
  ],
  "requires": [
    "tile.web-port",
    "tile.secrets",
    "tile.operator-secrets"
  ],
  "operatorSecrets": [
    {
      "name": "ais-key",
      "label": "AISStream API key"
    }
  ],
  "volumes": [
    {
      "name": "feeds-data",
      "backup": "state",
      "quiesce": "stop"
    }
  ]
}
```

`tiles/example-feeds/docker-compose.yml`:

<!-- tilelint-example: operator docker-compose.yml -->
```yaml
services:
  backend:
    image: ghcr.io/bigbodycobain/shadowbroker-backend:0.9.84@sha256:8b4bfdc01da491c37c0523ffbcb5dc3beb29324805abd35fcef436ae11511f18
    restart: unless-stopped
    environment:
      AIS_API_KEY: "${secret:ais-key}"
    volumes:
      - feeds-data:/app/data
    ports:
      - "8200:8000"

volumes:
  feeds-data:
```

Source of record (Geekdojo-internal, may not be public): S20,
[geekdojo/geekdojo-brain#719](https://github.com/geekdojo/geekdojo-brain/issues/719); the build is
[geekdojo/geekdojo-brain#780](https://github.com/geekdojo/geekdojo-brain/issues/780), and the
owner's screen for setting values is
[geekdojo/geekdojo-brain#781](https://github.com/geekdojo/geekdojo-brain/issues/781).

### R7: never write the token in a YAML comment

Do not write `${secret:` anywhere in a comment in the compose, not even as an example. Describe it
in words instead, such as "the platform's secret token".

Reason: the check reads the compose as plain text, comments included. A token in a comment is
checked and filled in like any other, and a placeholder such as `<name>` inside one fails tilelint,
because `<name>` is not a valid name.

Source of record (Geekdojo-internal, may not be public):
[ADR-0006](https://github.com/geekdojo/geekdojo-brain/blob/main/projects/rasputin/adr/0006-app-catalog-delivery.md)
Decision 4 amendment.

## Worked example

A recipe manager with a Postgres database. The `app` service and the `db` service must agree on
the database password, so both read the same token, `${secret:db-password}` (R1). The tile lists
`tile.secrets` (R2), and each named volume is declared in `tile.json` with its `backup` class and
`quiesce` strategy, as every tile must.

`tiles/example-recipes/tile.json`:

<!-- tilelint-example: tile.json -->
```json
{
  "id": "example-recipes",
  "name": "Example Recipes",
  "tagline": "A recipe box backed by its own database.",
  "description": "A worked example for the secrets guide: an app and its Postgres database sharing one platform-generated password.",
  "collection": "everyday",
  "category": "productivity",
  "arch": "both",
  "ramFloorMB": 768,
  "exposureDefault": "lan-only",
  "status": "preview",
  "ports": [
    {
      "name": "web",
      "container": 9000,
      "published": 9000,
      "protocol": "tcp",
      "web": true
    }
  ],
  "requires": [
    "tile.web-port",
    "tile.secrets"
  ],
  "volumes": [
    {
      "name": "recipes-data",
      "backup": "state",
      "quiesce": "stop"
    },
    {
      "name": "recipes-db",
      "backup": "state",
      "quiesce": "stop"
    }
  ]
}
```

`tiles/example-recipes/docker-compose.yml`:

<!-- tilelint-example: docker-compose.yml -->
```yaml
services:
  app:
    image: ghcr.io/mealie-recipes/mealie:v3.23.1@sha256:5fc5cebedddb3952c1ee78f20faf42ab7e49986813fd314745aa97978a4a13eb
    restart: unless-stopped
    depends_on:
      - db
    environment:
      DB_ENGINE: postgres
      POSTGRES_SERVER: db
      POSTGRES_PORT: "5432"
      POSTGRES_USER: recipes
      POSTGRES_DB: recipes
      POSTGRES_PASSWORD: "${secret:db-password}"
    volumes:
      - recipes-data:/app/data
    ports:
      - "9000:9000"

  db:
    image: postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685
    restart: unless-stopped
    environment:
      POSTGRES_USER: recipes
      POSTGRES_DB: recipes
      POSTGRES_PASSWORD: "${secret:db-password}"
    volumes:
      - recipes-db:/var/lib/postgresql/data

volumes:
  recipes-data:
  recipes-db:
```

Both worked examples, this one and R6's, are checked by this repository's tests on every pull
request, so each is a valid tile under the tile contract this repository builds against (the
`tileschema` version in `go.mod`).

## Checking your tile

Run tilelint on your tile from the repository root, where `<id>` is your tile's directory name:

```
go run ./cmd/tilelint -tile <id>
```

It prints one line per problem, starting with `x`, and ends with a count. It exits with status 0
when there are no problems and 1 when there are. Every secrets refusal ends with a sentence
pointing back to this guide. The secrets refusals, and the fix for each:

| Refusal (start of the message) | Fix |
|---|---|
| `compose uses ${secret:<name>} but the tile does not list "tile.secrets" in requires` | Add `tile.secrets` to `requires` in `tile.json` (R2). |
| `"$${secret:" is escaped with "$$"` | You wrote `$${secret:`, with two `$`. Write a single `$`: `${secret:<name>}`. |
| `${secret: has no closing brace on its line` | Close the token with `}` on the same line it opens on. |
| `secret name "<name>" must be a DNS-1123 label` | Rename the secret to lowercase `a`-`z`, `0`-`9` and `-`, 1 to 63 characters, no hyphen at either end (R1). For example, `DB_Pass` becomes `db-pass`. If the tile is already published, see R1 before renaming. |
| `the tile declares operatorSecrets but does not list "tile.operator-secrets" in requires` | Add `tile.operator-secrets` to `requires` in `tile.json` (R6). |
| `operatorSecrets[<i>]: name "<name>" must be a DNS-1123 label` | Rename the declared secret, and its token, by R1's rules (R6). |
| `operatorSecrets[<i>] "<name>": duplicate name` | Declare each operator-supplied secret once (R6). |
| `operatorSecrets[<i>] "<name>": label is required` | Give the declaration a `label`, the text the owner is shown (R6). |
| `operatorSecrets[<i>] "<name>": the compose never uses ${secret:<name>}` | Use the declared name as a token in the compose's `environment:`, or remove the declaration. Check for a typo: a token whose name differs from the declaration is a derived secret, not the owner's (R6). |

The token refusals (the second to fourth rows) are prefixed with `docker-compose.yml: byte <n>:`,
where `<n>` is how far into the file the bad token starts, counted in bytes from the beginning.
In the `operatorSecrets` refusals, `<i>` is the declaration's position in the list, counting
from 0.

## What the platform does

The control plane fills in each token's value when it deploys the tile, and its own database keeps
only the token, never the value. A deploy is refused while the cluster's secret store is not ready,
and while an operator-supplied secret the compose uses is not set.
