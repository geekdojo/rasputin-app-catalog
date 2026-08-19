// Command tilelint validates the tile corpus before it is published.
//
// It is a CALLER of the shared tileschema validators, never a second
// implementation of them (ADR-0006 Decision 8): a duplicate ruleset drifts, and
// the drift is silent because each side stays internally green. What lives here
// is only what genuinely cannot run on the control plane — deriving safety
// facts from compose, and asking a registry which platforms an image publishes.
//
//	tilelint            validate every tile (offline checks only)
//	tilelint -arch      also probe registries for architecture claims
//	tilelint -tile pi-hole
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/geekdojo/rasputin-app-catalog/internal/compose"
	"github.com/geekdojo/rasputin-app-catalog/internal/registry"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

func main() {
	var (
		root     = flag.String("root", "tiles", "tile corpus directory")
		probe    = flag.Bool("arch", false, "probe registries to verify architecture claims (network)")
		only     = flag.String("tile", "", "validate a single tile by id")
		failures int
		checked  int
	)
	flag.Parse()

	ids, err := tileIDs(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilelint:", err)
		os.Exit(2)
	}

	for _, id := range ids {
		if *only != "" && id != *only {
			continue
		}
		checked++
		for _, problem := range checkTile(*root, id, *probe) {
			fmt.Printf("  x %-22s %s\n", id, problem)
			failures++
		}
	}

	if *only != "" && checked == 0 {
		fmt.Fprintf(os.Stderr, "tilelint: no tile with id %q\n", *only)
		os.Exit(2)
	}
	fmt.Printf("\n%d tile(s) checked, %d problem(s)\n", checked, failures)
	if failures > 0 {
		os.Exit(1)
	}
}

// checkTile returns every problem with one tile rather than the first, so a
// contributor sees the whole list in one run instead of peeling them off across
// six pushes.
func checkTile(root, id string, probe bool) []string {
	var problems []string
	dir := filepath.Join(root, id)

	raw, err := os.ReadFile(filepath.Join(dir, "tile.json"))
	if err != nil {
		return []string{fmt.Sprintf("read tile.json: %v", err)}
	}
	var tile tileschema.Tile
	if err := json.Unmarshal(raw, &tile); err != nil {
		return []string{fmt.Sprintf("parse tile.json: %v", err)}
	}
	if tile.ID != id {
		problems = append(problems, fmt.Sprintf("id %q does not match its directory name", tile.ID))
	}

	composePath := filepath.Join(dir, "docker-compose.yml")
	composeYAML, composeErr := os.ReadFile(composePath)
	if composeErr == nil {
		tile.ComposeYAML = string(composeYAML)
	}

	if err := tileschema.ValidateTile(tile); err != nil {
		problems = append(problems, err.Error())
	}

	// Safety runs whenever a stack EXISTS, not only when the tile is available.
	// A preview tile with a compose file would otherwise carry an unchecked
	// stack behind the preview flag until the day someone flips it.
	if composeErr != nil {
		return problems
	}
	facts, err := compose.Extract(composeYAML)
	if err != nil {
		return append(problems, err.Error())
	}
	if err := tileschema.ValidateTileSafety(tile, facts); err != nil {
		problems = append(problems, err.Error())
	}

	if probe {
		problems = append(problems, archProblems(tile, facts)...)
	}
	return problems
}

// archProblems verifies the tile's arch claim against what the registry
// actually publishes. "both" is the claim worth checking hardest: it is the
// default a contributor reaches for, and getting it wrong means the tile
// installs happily and dies on every Pi in the fleet.
func archProblems(tile tileschema.Tile, facts tileschema.SafetyFacts) []string {
	want := map[string][]string{
		"both":  {"linux/amd64", "linux/arm64"},
		"arm64": {"linux/arm64"},
		"amd64": {"linux/amd64"},
	}[tile.Arch]

	var problems []string
	for _, img := range facts.Images {
		got, err := registry.Platforms(img)
		if err != nil {
			problems = append(problems, fmt.Sprintf("arch probe %s: %v", img, err))
			continue
		}
		if len(got) == 0 {
			problems = append(problems, fmt.Sprintf("%s publishes a single-architecture manifest; cannot satisfy arch %q", img, tile.Arch))
			continue
		}
		have := map[string]bool{}
		for _, p := range got {
			have[p] = true
		}
		for _, w := range want {
			if !have[w] {
				problems = append(problems, fmt.Sprintf("tile claims arch %q but %s publishes only %s", tile.Arch, img, strings.Join(got, ", ")))
				break
			}
		}
	}
	return problems
}

func tileIDs(root string) ([]string, error) {
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
