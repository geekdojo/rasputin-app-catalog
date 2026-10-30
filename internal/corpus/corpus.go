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
//
// TileJSON is the tile.json bytes exactly as committed. Tile is decoded from
// them and is what every validator judges; TileJSON is what the bundle
// publishes, so a field the pinned tileschema does not know yet still reaches
// the cluster instead of being dropped by the struct (ADR-0012 Decision 6).
type Loaded struct {
	Tile       tileschema.Tile
	TileJSON   json.RawMessage
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

	out := Loaded{Tile: tile, TileJSON: raw}
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

// Bundle is the document catalogbundle signs.
//
// It embeds tileschema.Bundle so the envelope fields are not restated here and
// a field the module adds flows through. Tiles shadows the embedded list: the
// embedded one is the struct form that was validated, and BuildBundle leaves
// it nil so the published list is the only one on the result.
type Bundle struct {
	tileschema.Bundle
	Tiles []PublishedTile `json:"tiles"`
}

// PublishedTile is one tile as published: the author's tile.json bytes, never
// the struct re-encoded (ADR-0012 Decision 6), beside the compose and safety
// facts. The embedded BundleTile's own Tile is the decoded form, kept for
// validation only; the outer Tile wins in JSON.
type PublishedTile struct {
	Tile json.RawMessage `json:"tile"`
	tileschema.BundleTile
}

// Encode renders the bundle as the bytes that get signed: indented, with a
// trailing newline.
//
// Indented, not compact. The artifact is signed over its bytes either way, and
// a catalog someone can read in a browser during an incident is worth more
// than the few kilobytes compaction saves on a daily poll. encoding/json
// re-indents each tile's raw bytes, so the author's insignificant whitespace
// and line endings do not reach the output; key order and number literals do.
func (b Bundle) Encode() ([]byte, error) {
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode catalog bundle: %w", err)
	}
	return append(body, '\n'), nil
}

// BuildBundle turns the corpus into the publishable, signable document.
//
// It REFUSES to emit an invalid bundle. A publisher that can produce something
// the control plane will reject has moved the failure from a CI log to a
// cluster, and the signature would make the broken artifact look authoritative
// on the way.
//
// Tiles come out in ID order without an explicit sort: IDs sorts, and Load
// refuses a tile whose id differs from its directory name.
func BuildBundle(root string, version int, publishedAt, source string) (Bundle, error) {
	ids, err := IDs(root)
	if err != nil {
		return Bundle{}, err
	}

	tiles := make([]tileschema.BundleTile, 0, len(ids))
	published := make([]PublishedTile, 0, len(ids))
	for _, id := range ids {
		l, err := Load(root, id)
		if err != nil {
			return Bundle{}, fmt.Errorf("tile %q: %w", id, err)
		}

		bt := tileschema.BundleTile{Tile: l.Tile, Compose: l.Compose}
		// The wire format carries compose beside the tile, not inside it.
		bt.Tile.ComposeYAML = ""

		if l.HasCompose {
			facts, err := compose.Extract([]byte(l.Compose))
			if err != nil {
				return Bundle{}, fmt.Errorf("tile %q: %w", id, err)
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
				return Bundle{}, fmt.Errorf("tile %q: %w", id, err)
			}
			if problems := compose.ClassificationProblems(l.Tile, stack); len(problems) > 0 {
				return Bundle{}, fmt.Errorf("tile %q: refusing to publish an unclassified corpus: %s", id, problems[0])
			}
		}
		tiles = append(tiles, bt)
		published = append(published, PublishedTile{Tile: l.TileJSON, BundleTile: bt})
	}

	b := tileschema.Bundle{
		SchemaVersion: tileschema.BundleSchemaVersion,
		Version:       version,
		PublishedAt:   publishedAt,
		Source:        source,
		Tiles:         tiles,
	}
	if err := b.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("refusing to publish an invalid bundle: %w", err)
	}
	b.Tiles = nil
	return Bundle{Bundle: b, Tiles: published}, nil
}
