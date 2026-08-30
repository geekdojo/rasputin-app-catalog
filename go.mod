module github.com/geekdojo/rasputin-app-catalog

go 1.26

// ⚠ TEMPORARY PIN: the tileschema version below is a commit on the
// `tile-web-port` BRANCH of rasputin-control-plane (PR #197), not on main.
// It is what lets this corpus migrate to the web-port field before that PR
// merges — which it must, because the control plane's signed offline floor is
// regenerated from a release of THIS repo, so #197 cannot merge until we have
// published one. Re-pin to a main commit once #197 lands.
// geekdojo/geekdojo-brain#387, #388.
require (
	github.com/geekdojo/rasputin-control-plane/tileschema v0.0.0-20260830165455-0728fa01a02e
	gopkg.in/yaml.v3 v3.0.1
)
