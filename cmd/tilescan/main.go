// Command tilescan is the supply-chain gate: it scans every shipping tile's
// pinned image and enforces what carrying that tile commits us to.
//
// Split from tilelint deliberately. tilelint is offline, deterministic and
// gates every PR; tilescan pulls images and consults a vulnerability database,
// so it is slow, network-dependent, and its answer CHANGES for code that did
// not. Those belong on different triggers, and merging them would make the fast
// gate hostage to Docker Hub.
//
//	tilescan            verify provenance matches, and gate on vulnerabilities
//	tilescan -write     refresh the checked-in provenance (review the diff!)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/geekdojo/rasputin-app-catalog/internal/scan"
)

var imageRe = regexp.MustCompile(`(?m)^\s*image:\s*(\S+)`)

func main() {
	write := flag.Bool("write", false, "refresh checked-in provenance instead of verifying it")
	root := flag.String("root", "tiles", "tile corpus directory")
	provDir := flag.String("provenance", "provenance", "checked-in provenance directory")
	flag.Parse()

	tiles, err := shippingTiles(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tilescan:", err)
		os.Exit(2)
	}
	if len(tiles) == 0 {
		fmt.Fprintln(os.Stderr, "tilescan: no shipping tiles found — refusing to pass trivially")
		os.Exit(2)
	}

	failures := 0
	for _, t := range tiles {
		for _, img := range t.images {
			prov, vulns, err := scan.Scan(t.id, img)
			if err != nil {
				fmt.Printf("  x %-18s %v\n", t.id, err)
				failures++
				continue
			}
			failures += report(t.id, img, prov, vulns, *provDir, *write)
		}
	}

	fmt.Printf("\n%d tile(s) scanned, %d problem(s)\n", len(tiles), failures)
	if failures > 0 && !*write {
		os.Exit(1)
	}
}

func report(id, img string, prov scan.Provenance, vulns []scan.Vuln, provDir string, write bool) int {
	failures := 0
	path := filepath.Join(provDir, id+".json")

	if write {
		blob, _ := json.MarshalIndent(prov, "", "  ")
		if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
			fmt.Printf("  x %-18s write provenance: %v\n", id, err)
			return 1
		}
		fmt.Printf("  ~ %-18s provenance written\n", id)
	} else {
		old, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  x %-18s no checked-in provenance (%v) — run -write and review the diff\n", id, err)
			failures++
		} else {
			var prev scan.Provenance
			if err := json.Unmarshal(old, &prev); err != nil {
				fmt.Printf("  x %-18s unreadable provenance: %v\n", id, err)
				failures++
			} else if !reflect.DeepEqual(prev, prov) {
				// A re-pin that changes what the image contains is a DECISION,
				// not a version bump. This is the mechanism behind the app
				// catalog policy that treats a licence change on a tile we ship
				// as needing a decision rather than a note.
				fmt.Printf("  x %-18s provenance drift — review before accepting:\n", id)
				fmt.Printf("      was  source-available=%v copyleft=%v\n", prev.SourceAvailable, prev.Copyleft)
				fmt.Printf("      now  source-available=%v copyleft=%v\n", prov.SourceAvailable, prov.Copyleft)
				failures++
			}
		}
	}

	// Source-available licences are not open source and change what we may
	// ship. Finding one is a hard stop whether or not it is new.
	if len(prov.SourceAvailable) > 0 {
		fmt.Printf("  x %-18s SOURCE-AVAILABLE licence present: %s\n", id, strings.Join(prov.SourceAvailable, ", "))
		failures++
	}

	var fixable []scan.Vuln
	for _, v := range vulns {
		if v.Fixable() {
			fixable = append(fixable, v)
		}
	}
	if len(fixable) > 0 {
		// Fixable means upstream shipped a fix and OUR pin is stale — the one
		// class of vulnerability we can actually act on.
		fmt.Printf("  x %-18s %d fixable HIGH/CRITICAL — the pin is stale:\n", id, len(fixable))
		for _, v := range fixable[:min(5, len(fixable))] {
			fmt.Printf("      %s %s %s → %s (%s)\n", v.Severity, v.Pkg, v.Installed, v.Fixed, v.ID)
		}
		if len(fixable) > 5 {
			fmt.Printf("      … and %d more\n", len(fixable)-5)
		}
		failures++
	}
	if unfixed := len(vulns) - len(fixable); unfixed > 0 {
		// Reported, never gated: no upstream fix exists, so the only lever is
		// dropping the tile. A permanently red build nobody can green is a
		// build everyone learns to ignore.
		fmt.Printf("  · %-18s %d unfixed HIGH/CRITICAL (no upstream fix — reported, not gated)\n", id, unfixed)
	}
	return failures
}

type tile struct {
	id     string
	images []string
}

// shippingTiles returns tiles that can actually be installed. A preview tile
// carries no compose and therefore no images to scan.
func shippingTiles(root string) ([]tile, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []tile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, e.Name(), "docker-compose.yml"))
		if err != nil {
			continue // preview tile: metadata only
		}
		var imgs []string
		for _, m := range imageRe.FindAllStringSubmatch(string(body), -1) {
			imgs = append(imgs, m[1])
		}
		if len(imgs) > 0 {
			sort.Strings(imgs)
			out = append(out, tile{id: e.Name(), images: imgs})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}
