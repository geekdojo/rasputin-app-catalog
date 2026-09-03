// Package corpus reads the tile tree on disk.
//
// It exists so the linter and the bundle builder share ONE loader. They ask
// slightly different questions of a tile — the linter wants every problem, the
// builder wants a publishable record — but they must agree on what a tile IS,
// where its files live, and how its compose becomes safety facts. Two loaders
// would drift, and the drift would be silent: the linter would pass a corpus
// the builder then published differently. That is the same failure ADR-0006
// Decision 8 rejected across the two repos, applied inside this one.
package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/geekdojo/rasputin-app-catalog/internal/compose"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// TileFile and ComposeFile are the two files that make a tile directory.
const (
	TileFile    = "tile.json"
	ComposeFile = "docker-compose.yml"
)

// IDs lists the tile directories under root, sorted.
func IDs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no tiles found under %s", root)
	}
	sort.Strings(ids)
	return ids, nil
}

// Loaded is one tile as it exists on disk, before any judgement is passed on
// it. Compose is empty for a preview tile, which is allowed to ship metadata
// only — HasCompose says which case this is, so callers do not have to infer
// it from an empty string.
type Loaded struct {
	Tile       tileschema.Tile
	Compose    string
	HasCompose bool
}

// Load reads one tile directory. It reports structural problems — unreadable
// or unparseable files, an id that disagrees with its directory — and leaves
// every POLICY question to tileschema's validators. Keeping the split here is
// what lets the linter and the builder disagree about severity without
// disagreeing about facts.
func Load(root, id string) (Loaded, error) {
	dir := filepath.Join(root, id)

	raw, err := os.ReadFile(filepath.Join(dir, TileFile))
	if err != nil {
		return Loaded{}, fmt.Errorf("read %s: %w", TileFile, err)
	}
	var tile tileschema.Tile
	if err := json.Unmarshal(raw, &tile); err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", TileFile, err)
	}
	if tile.ID != id {
		return Loaded{}, fmt.Errorf("id %q does not match its directory name %q", tile.ID, id)
	}

	out := Loaded{Tile: tile}
	composeYAML, err := os.ReadFile(filepath.Join(dir, ComposeFile))
	switch {
	case err == nil:
		out.Compose = string(composeYAML)
		out.HasCompose = true
		out.Tile.ComposeYAML = out.Compose
	case errors.Is(err, os.ErrNotExist):
		// A preview tile may ship metadata only.
	default:
		return Loaded{}, fmt.Errorf("read %s: %w", ComposeFile, err)
	}
	return out, nil
}

// BuildBundle turns the corpus into the publishable, signable document.
//
// It REFUSES to emit an invalid bundle. A publisher that can produce something
// the control plane will reject has moved the failure from a CI log to a
// cluster, and the signature would make the broken artifact look authoritative
// on the way.
func BuildBundle(root string, version int, publishedAt, source string) (tileschema.Bundle, error) {
	ids, err := IDs(root)
	if err != nil {
		return tileschema.Bundle{}, err
	}

	tiles := make([]tileschema.BundleTile, 0, len(ids))
	for _, id := range ids {
		l, err := Load(root, id)
		if err != nil {
			return tileschema.Bundle{}, fmt.Errorf("tile %q: %w", id, err)
		}

		bt := tileschema.BundleTile{Tile: l.Tile, Compose: l.Compose}
		// The wire format carries compose beside the tile, not inside it.
		bt.Tile.ComposeYAML = ""

		if l.HasCompose {
			facts, err := compose.Extract([]byte(l.Compose))
			if err != nil {
				return tileschema.Bundle{}, fmt.Errorf("tile %q: %w", id, err)
			}
			bt.Safety = facts

			// Volume coverage, enforced on the PUBLISH path and not only in
			// the linter. b.Validate() below refuses a declared volume with a
			// missing class, but it cannot see a volume the stack creates and
			// the tile never named — the control plane parses no YAML
			// (Decision 4), so that fact does not survive into the bundle for
			// it to check. If this were left to tilelint alone, a corpus that
			// fails the lint could still be signed and shipped by a dispatch
			// that skipped it, and the signature would make it authoritative.
			stack, err := compose.Volumes([]byte(l.Compose))
			if err != nil {
				return tileschema.Bundle{}, fmt.Errorf("tile %q: %w", id, err)
			}
			if problems := compose.ClassificationProblems(l.Tile, stack); len(problems) > 0 {
				return tileschema.Bundle{}, fmt.Errorf("tile %q: refusing to publish an unclassified corpus: %s", id, problems[0])
			}
		}
		tiles = append(tiles, bt)
	}
	tileschema.SortTiles(tiles)

	b := tileschema.Bundle{
		SchemaVersion: tileschema.BundleSchemaVersion,
		Version:       version,
		PublishedAt:   publishedAt,
		Source:        source,
		Tiles:         tiles,
	}
	if err := b.Validate(); err != nil {
		return tileschema.Bundle{}, fmt.Errorf("refusing to publish an invalid bundle: %w", err)
	}
	return b, nil
}
