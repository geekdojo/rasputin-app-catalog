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
	"os/exec"
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
	baseline := flag.String("baseline", "", "git ref to compare against; scans the OLD and NEW pin of each changed tile in the same run")
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

	if *baseline != "" {
		os.Exit(compareAgainst(*baseline, tiles))
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
				// catalog policy that treats a license change on a tile we ship
				// as needing a decision rather than a note.
				fmt.Printf("  x %-18s provenance drift — review before accepting:\n", id)
				fmt.Printf("      was  source-available=%v copyleft=%v\n", prev.SourceAvailable, prev.Copyleft)
				fmt.Printf("      now  source-available=%v copyleft=%v\n", prov.SourceAvailable, prov.Copyleft)
				failures++
			}
		}
	}

	// Source-available licenses are not open source and change what we may
	// ship. Finding one is a hard stop whether or not it is new.
	if len(prov.SourceAvailable) > 0 {
		fmt.Printf("  x %-18s SOURCE-AVAILABLE license present: %s\n", id, strings.Join(prov.SourceAvailable, ", "))
		failures++
	}

	var appFix, osFix, unfixed int
	var worst []scan.Vuln
	for _, v := range vulns {
		switch {
		case !v.Fixable():
			unfixed++
		case v.AppLevel():
			appFix++
			worst = append(worst, v)
		default:
			osFix++
		}
	}

	// Split by owner, because a single total answers the wrong question. App-level
	// findings are the maintainers' own dependency choices and say something real
	// about the project; base-image findings come from whatever they built on and
	// are fixed by an upstream rebuild. Summing them once nearly moved this catalog
	// onto a branch that had shipped a single release in twenty months.
	if appFix > 0 {
		fmt.Printf("  x %-18s %d fixable HIGH/CRITICAL in the app's OWN dependencies:\n", id, appFix)
		for _, v := range worst[:min(5, len(worst))] {
			fmt.Printf("      %s %s %s → %s (%s)\n", v.Severity, v.Pkg, v.Installed, v.Fixed, v.ID)
		}
		if len(worst) > 5 {
			fmt.Printf("      … and %d more\n", len(worst)-5)
		}
		failures++
	}
	if osFix > 0 {
		// Reported, not gated: the remedy is upstream rebuilding their base image.
		fmt.Printf("  · %-18s %d fixable in the base image (upstream's rebuild, not ours)\n", id, osFix)
	}
	if unfixed > 0 {
		fmt.Printf("  · %-18s %d unfixed HIGH/CRITICAL (no upstream fix anywhere — reported, not gated)\n", id, unfixed)
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

// compareAgainst is the gate that makes unattended updates safe.
//
// It scans the OLD and the NEW pin of every changed tile IN THE SAME RUN, and
// fails only if the app's own fixable count went UP. Two properties matter:
//
//   - Same run means the same CVE database for both sides. Comparing against a
//     stored number is flaky by construction, because the count for an unchanged
//     digest moves whenever the database does — a gate that fails for reasons no
//     PR caused is a gate people learn to bypass.
//
//   - App-level only. Base-image findings are the upstream's rebuild to do, and
//     gating on them would have blocked uptime-kuma 2.5.0, which is strictly
//     better on the dependencies its maintainers actually chose.
//
// A ratchet, not an absolute bar: 169 app-level findings across five tiles is
// today's floor, so "no known vulnerabilities" would be a permanently red build.
// This says instead: an update may not make things worse.
func compareAgainst(ref string, tiles []tile) int {
	worse, checked := 0, 0
	for _, t := range tiles {
		oldImages, err := imagesAtRef(ref, t.id)
		if err != nil || len(oldImages) == 0 {
			// A NEW TILE IS NOT A REGRESSION. Counting it as one made this gate
			// unsatisfiable for the thing the intake pipeline exists to produce:
			// supply-chain is a required check, so no new tile could ever merge.
			// Caught by the first live agent run (#165 validating against
			// linkding), because no amount of local testing had ever added a tile.
			//
			// Scan it in full and REPORT, so a reviewer sees the numbers they are
			// being asked to accept. Fail only on what is wrong in absolute terms.
			checked++
			worse += reportNewTile(t)
			continue
		}
		for i, img := range t.images {
			if i >= len(oldImages) || oldImages[i] == img {
				continue // unchanged pin: nothing to compare
			}
			checked++
			_, newV, err1 := scan.Scan(t.id, img)
			_, oldV, err2 := scan.Scan(t.id, oldImages[i])
			if err1 != nil || err2 != nil {
				fmt.Printf("  x %-18s scan failed (%v / %v)\n", t.id, err1, err2)
				worse++
				continue
			}
			was, now := appFixable(oldV), appFixable(newV)
			if ceiling, ok := acceptedCeiling(t.id); ok && now <= ceiling && now > was {
				fmt.Printf("  ~ %-18s app-level fixable %d → %d, within the recorded ceiling of %d\n", t.id, was, now, ceiling)
				continue
			}
			switch {
			case now > was:
				fmt.Printf("  x %-18s app-level fixable ROSE %d → %d — this update makes it worse\n", t.id, was, now)
				fmt.Printf("      accept it by setting appFixableCeiling + acceptedReason in provenance/%s.json\n", t.id)
				worse++
			case now < was:
				fmt.Printf("  ✓ %-18s app-level fixable %d → %d\n", t.id, was, now)
			default:
				fmt.Printf("  ✓ %-18s app-level fixable unchanged at %d\n", t.id, now)
			}
		}
	}
	if checked == 0 && worse == 0 {
		fmt.Printf("\nno tile images changed against %s\n", ref)
		return 0
	}
	fmt.Printf("\n%d changed image(s) compared against %s, %d regression(s)\n", checked, ref, worse)
	if worse > 0 {
		return 1
	}
	return 0
}

// acceptedCeiling reads a reviewed exception from the checked-in provenance.
// Absent or unreasoned means no exception, which is the safe default.
func acceptedCeiling(id string) (int, bool) {
	blob, err := os.ReadFile(filepath.Join("provenance", id+".json"))
	if err != nil {
		return 0, false
	}
	var p scan.Provenance
	if json.Unmarshal(blob, &p) != nil {
		return 0, false
	}
	return p.Ceiling()
}

// reportNewTile scans a tile that has no baseline and prints what a reviewer is
// being asked to take on. It returns 1 only for a genuine defect — a
// source-available license — never for the tile merely being new.
func reportNewTile(t tile) int {
	bad := 0
	for _, img := range t.images {
		prov, vulns, err := scan.Scan(t.id, img)
		if err != nil {
			fmt.Printf("  x %-18s %v\n", t.id, err)
			bad++
			continue
		}
		app, base, unfixed := 0, 0, 0
		for _, v := range vulns {
			switch {
			case !v.Fixable():
				unfixed++
			case v.AppLevel():
				app++
			default:
				base++
			}
		}
		fmt.Printf("  + %-18s NEW — %d app-level fixable, %d base-image, %d unfixed\n", t.id, app, base, unfixed)
		if len(prov.SourceAvailable) > 0 {
			fmt.Printf("  x %-18s SOURCE-AVAILABLE license present: %s\n", t.id, strings.Join(prov.SourceAvailable, ", "))
			bad++
		}
		if len(prov.Copyleft) > 0 {
			fmt.Printf("  · %-18s copyleft present: %s\n", t.id, strings.Join(prov.Copyleft[:min(4, len(prov.Copyleft))], ", "))
		}
		fmt.Printf("  · %-18s before merging: run `go run ./cmd/tilescan -write` and review provenance/%s.json\n", t.id, t.id)
	}
	return bad
}

func appFixable(vs []scan.Vuln) int {
	n := 0
	for _, v := range vs {
		if v.Fixable() && v.AppLevel() {
			n++
		}
	}
	return n
}

func imagesAtRef(ref, id string) ([]string, error) {
	out, err := exec.Command("git", "show", fmt.Sprintf("%s:tiles/%s/docker-compose.yml", ref, id)).Output()
	if err != nil {
		return nil, err
	}
	var imgs []string
	for _, m := range imageRe.FindAllStringSubmatch(string(out), -1) {
		imgs = append(imgs, m[1])
	}
	sort.Strings(imgs)
	return imgs, nil
}
