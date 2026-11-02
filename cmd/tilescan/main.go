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
	"bytes"
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
		c := comparer{scan: scan.Scan, imagesAt: imagesAtRef, ceiling: acceptedCeiling}
		os.Exit(c.compareAgainst(*baseline, tiles))
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

	// AppFixableCeiling and AcceptedReason live ONLY in the checked-in file —
	// they record a human decision and scan.Scan never produces them. Carry
	// them onto the scanned struct before it is written or compared, because
	// the asymmetry breaks BOTH paths:
	//
	//   -write   marshals the scanned struct straight over the file, silently
	//            deleting an accepted exception and the revisit trigger with
	//            it — and the tool's own output tells reviewers to run -write
	//            before merging, so the deletion looks routine in the diff.
	//   verify   DeepEqual-compares a file that HAS the fields against a scan
	//            that never does, so any tile carrying an exception reports
	//            permanent "provenance drift" while printing source-available
	//            and copyleft lines that are identical — an alarm with nothing
	//            to act on, which is how a gate stops being read.
	//
	// Preserving them here keeps the exception a reviewed diff, which is the
	// whole point of storing it in the file rather than in a baseline.
	if old, err := os.ReadFile(path); err == nil {
		var prev scan.Provenance
		if json.Unmarshal(old, &prev) == nil {
			prov.AppFixableCeiling = prev.AppFixableCeiling
			prov.AcceptedReason = prev.AcceptedReason
		}
	}

	if write {
		blob, err := encodeProvenance(prov)
		if err != nil {
			fmt.Printf("  x %-18s encode provenance: %v\n", id, err)
			return 1
		}
		if err := os.WriteFile(path, blob, 0o644); err != nil {
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

// scanFunc is the shape of scan.Scan. The comparison takes it as a
// collaborator so the pairing logic can be tested without pulling images.
type scanFunc func(tile, image string) (scan.Provenance, []scan.Vuln, error)

// comparer holds the baseline gate's collaborators. main wires the real ones:
// scan.Scan, imagesAtRef and acceptedCeiling.
type comparer struct {
	scan     scanFunc
	imagesAt func(ref, id string) ([]string, error)
	ceiling  func(id string) (int, bool)
}

// compareAgainst is the gate that makes unattended updates safe.
//
// It scans the OLD and the NEW pin of every changed image IN THE SAME RUN, and
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
func (c comparer) compareAgainst(ref string, tiles []tile) int {
	worse, checked := 0, 0
	for _, t := range tiles {
		oldImages, err := c.imagesAt(ref, t.id)
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
			worse += c.reportNewTile(t)
			continue
		}
		changes, added := pairImages(oldImages, t.images)
		for _, img := range added {
			// A repository the tile did not carry before has no old pin to
			// ratchet against, so it is treated exactly as a new tile's image.
			checked++
			worse += c.reportNewImage(t.id, img)
		}
		for _, ch := range changes {
			checked++
			_, newV, err1 := c.scan(t.id, ch.now)
			_, oldV, err2 := c.scan(t.id, ch.was)
			if err1 != nil || err2 != nil {
				fmt.Printf("  x %-18s scan failed (%v / %v)\n", t.id, err1, err2)
				worse++
				continue
			}
			was, now := appFixable(oldV), appFixable(newV)
			if ceiling, ok := c.ceiling(t.id); ok && now <= ceiling && now > was {
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

// pinChange is one image whose pin moved: the old reference and the new one.
type pinChange struct{ was, now string }

// pairImages matches a tile's old pins to its new pins by image repository.
//
// Pairing by position in the sorted list was wrong: a tile that runs the same
// image twice (an app plus a one-shot job on its database image) shifts every
// index after the duplicate, so unrelated images were compared — a false
// regression where they differ, and a silently skipped image at the end.
//
// Pins are compared as sets, because a second copy of an image the tile already
// ran adds nothing to scan. Each new pin not in the old set is paired with an
// old pin of the same repository that went away; failing that, with an old pin
// of that repository that is still present (a second, different tag). A pin
// whose repository the tile never carried is returned in added.
func pairImages(oldImages, newImages []string) (changes []pinChange, added []string) {
	oldSet, newSet := pinSet(oldImages), pinSet(newImages)
	removed := map[string][]string{} // repository → old pins no longer present
	kept := map[string][]string{}    // repository → old pins still present
	for _, img := range sortedKeys(oldSet) {
		r := repository(img)
		if newSet[img] {
			kept[r] = append(kept[r], img)
		} else {
			removed[r] = append(removed[r], img)
		}
	}
	for _, img := range sortedKeys(newSet) {
		if oldSet[img] {
			continue // unchanged pin: nothing to compare
		}
		r := repository(img)
		switch {
		case len(removed[r]) > 0:
			changes = append(changes, pinChange{was: removed[r][0], now: img})
			removed[r] = removed[r][1:]
		case len(kept[r]) > 0:
			changes = append(changes, pinChange{was: kept[r][0], now: img})
		default:
			added = append(added, img)
		}
	}
	return changes, added
}

func pinSet(imgs []string) map[string]bool {
	set := make(map[string]bool, len(imgs))
	for _, img := range imgs {
		set[img] = true
	}
	return set
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// repository strips the digest and tag from an image reference. A colon before
// the last slash is a registry port, not a tag.
func repository(img string) string {
	if at := strings.Index(img, "@"); at >= 0 {
		img = img[:at]
	}
	if colon := strings.LastIndex(img, ":"); colon > strings.LastIndex(img, "/") {
		img = img[:colon]
	}
	return img
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
func (c comparer) reportNewTile(t tile) int {
	bad := 0
	for _, img := range t.images {
		bad += c.reportNewImage(t.id, img)
	}
	return bad
}

// reportNewImage scans one image that has no old pin to compare against.
func (c comparer) reportNewImage(id, img string) int {
	prov, vulns, err := c.scan(id, img)
	if err != nil {
		fmt.Printf("  x %-18s %v\n", id, err)
		return 1
	}
	bad := 0
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
	fmt.Printf("  + %-18s NEW — %d app-level fixable, %d base-image, %d unfixed\n", id, app, base, unfixed)
	if len(prov.SourceAvailable) > 0 {
		fmt.Printf("  x %-18s SOURCE-AVAILABLE license present: %s\n", id, strings.Join(prov.SourceAvailable, ", "))
		bad++
	}
	if len(prov.Copyleft) > 0 {
		fmt.Printf("  · %-18s copyleft present: %s\n", id, strings.Join(prov.Copyleft[:min(4, len(prov.Copyleft))], ", "))
	}
	fmt.Printf("  · %-18s before merging: run `go run ./cmd/tilescan -write` and review provenance/%s.json\n", id, id)
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

// encodeProvenance renders a provenance file the way a reviewer should see it.
//
// json.MarshalIndent escapes <, > and & as <, > and &, because
// its default is to be safe to embed in HTML. Nothing here is ever embedded in
// HTML, and acceptedReason is PROSE — the audiobookshelf entry alone contains
// two "->" arrows. Escaping rewrote that line on every -write, so a reviewer
// diffing a rescan saw a mangled paragraph move whether or not anything about
// the image had changed. Churn in the exact field this file exists to make
// reviewable is how the diff stops being read, which is the failure the
// carry-across above was fixing in the first place.
func encodeProvenance(p scan.Provenance) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil { // Encode already appends the newline
		return nil, err
	}
	return buf.Bytes(), nil
}
