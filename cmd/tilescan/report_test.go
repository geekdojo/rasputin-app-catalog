package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-app-catalog/internal/scan"
)

// The reviewed exception is the only part of a provenance file a human wrote.
// scan.Scan cannot reproduce it, so every path that writes or compares the file
// has to carry it across explicitly. These cover both directions.

func writeProv(t *testing.T, dir, id string, p scan.Provenance) {
	t.Helper()
	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), append(blob, '\n'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readProv(t *testing.T, dir, id string) scan.Provenance {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var p scan.Provenance
	if err := json.Unmarshal(blob, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

// -write used to marshal the freshly-scanned struct straight over the file,
// deleting a decision someone made. The tool tells reviewers to run -write
// before merging, so the deletion arrived looking routine.
func TestReport_WritePreservesAcceptedException(t *testing.T) {
	dir := t.TempDir()
	const id = "audiobookshelf"
	const reason = "count rose 60 -> 62; staying put fixes none of the other 60. Revisit at the next bench."

	writeProv(t, dir, id, scan.Provenance{
		Tile: id, Image: "old@sha256:aa", Copyleft: []string{"GPL-3.0-or-later"},
		AppFixableCeiling: 62, AcceptedReason: reason,
	})

	// What a rescan produces: no ceiling, no reason — Scan cannot know them.
	scanned := scan.Provenance{Tile: id, Image: "new@sha256:bb", Copyleft: []string{"GPL-3.0-or-later"}}
	if got := report(id, "new@sha256:bb", scanned, nil, dir, true); got != 0 {
		t.Fatalf("report(write) failures = %d, want 0", got)
	}

	got := readProv(t, dir, id)
	if got.AppFixableCeiling != 62 {
		t.Errorf("ceiling = %d, want 62 — -write destroyed a reviewed exception", got.AppFixableCeiling)
	}
	if got.AcceptedReason != reason {
		t.Errorf("acceptedReason = %q, want it preserved", got.AcceptedReason)
	}
	if got.Image != "new@sha256:bb" {
		t.Errorf("image = %q, want the rescanned value — the scan half must still update", got.Image)
	}
}

// Verify mode DeepEqual-compared a file that HAS the exception against a scan
// that never does, so a tile carrying one reported drift forever while printing
// identical source-available and copyleft lines.
func TestReport_VerifyDoesNotFlagExceptionAsDrift(t *testing.T) {
	dir := t.TempDir()
	const id = "audiobookshelf"

	prov := scan.Provenance{Tile: id, Image: "img@sha256:aa", Copyleft: []string{"GPL-3.0-or-later"}}
	stored := prov
	stored.AppFixableCeiling = 62
	stored.AcceptedReason = "reviewed and accepted"
	writeProv(t, dir, id, stored)

	if got := report(id, "img@sha256:aa", prov, nil, dir, false); got != 0 {
		t.Errorf("report(verify) failures = %d, want 0 — the exception is not drift", got)
	}
}

// The drift alarm must still fire on what it exists to catch: a re-pin that
// changes the licences the image carries.
func TestReport_VerifyStillFlagsRealDrift(t *testing.T) {
	dir := t.TempDir()
	const id = "somewhere"

	writeProv(t, dir, id, scan.Provenance{
		Tile: id, Image: "img@sha256:aa", Copyleft: []string{"GPL-3.0-or-later"},
		AppFixableCeiling: 5, AcceptedReason: "reviewed",
	})

	changed := scan.Provenance{
		Tile: id, Image: "img@sha256:bb",
		SourceAvailable: []string{"BUSL-1.1"},
		Copyleft:        []string{"GPL-3.0-or-later"},
	}
	if got := report(id, "img@sha256:bb", changed, nil, dir, false); got == 0 {
		t.Error("report(verify) failures = 0, want >0 — a licence change must still be caught")
	}
}

// acceptedReason is prose. The audiobookshelf entry alone contains two "->"
// arrows, and MarshalIndent's default HTML escaping rewrote that whole line on
// every -write — churn in the one field the file exists to make reviewable.
func TestReport_WriteDoesNotHTMLEscapeProse(t *testing.T) {
	dir := t.TempDir()
	const id = "audiobookshelf"
	const reason = "2.17.0 -> 2.36.0 adds two CVEs & removes none, so the count rose 60 -> 62."

	writeProv(t, dir, id, scan.Provenance{Tile: id, Image: "old@sha256:aa", AppFixableCeiling: 62, AcceptedReason: reason})
	scanned := scan.Provenance{Tile: id, Image: "new@sha256:bb"}
	if got := report(id, "new@sha256:bb", scanned, nil, dir, true); got != 0 {
		t.Fatalf("report(write) failures = %d, want 0", got)
	}

	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, esc := range []string{`\u003e`, `\u003c`, `\u0026`} {
		if strings.Contains(string(raw), esc) {
			t.Errorf("file contains %s — prose was HTML-escaped", esc)
		}
	}
	if !strings.Contains(string(raw), "-> 2.36.0") || !strings.Contains(string(raw), "CVEs & removes") {
		t.Errorf("prose not written literally:\n%s", raw)
	}
	// Exactly one trailing newline — Encode appends its own.
	if !bytes.HasSuffix(raw, []byte("}\n")) || bytes.HasSuffix(raw, []byte("\n\n")) {
		t.Errorf("want exactly one trailing newline, got %q", raw[max(0, len(raw)-4):])
	}
}
