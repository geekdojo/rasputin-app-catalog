// Package legacybridge holds the tests of the one-shot bridge services in the
// romm and immich tiles (romm-db-bridge, immich-db-bridge). It has no code of
// its own: the bridges are shell inside the tile composes.
//
// Both databases apply their password only when they first create their data
// directory, so an install upgraded from a catalog version that shipped
// published passwords keeps them. On every deploy the bridge checks whether the
// app's account already logs in with the platform's value; if not, it logs in
// with the published legacy value and changes the passwords to the platform's.
//
// Two kinds of test live here:
//
//   - Unit tests, run by `go test ./...` on every pull request. They check the
//     compose text: which values are tokens, where the legacy values may
//     appear, the bridge's shape, and the R4 row in EXCEPTIONS.md.
//
//   - Functional tests, behind the `functional` build tag. They read the real
//     tile composes, replace each token with the Compose interpolation
//     reference the control plane sends, and drive `docker compose` against
//     the pinned images: fresh install, upgrade from the published passwords,
//     re-deploy, compose stop/start with a pre-upgrade copy of the database
//     restored, revert to the previous compose and recovery by upgrading
//     again, a database on unknown passwords, and a database that never
//     answers. They need a running Docker daemon and the registries, and fail
//     rather than skip without them. Run them from the repository root with
//
//     go test -tags functional ./internal/legacybridge/
//
//     CI runs them in the lint workflow's arch job (push to main and weekly).
//
// What they do not prove: the control plane's resolve, store and push of the
// values, the agent's handling of the bridge's exit, and the karakeep keys.
package legacybridge
