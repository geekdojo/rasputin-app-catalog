package main

import (
	"fmt"
	"testing"

	"github.com/geekdojo/rasputin-app-catalog/internal/scan"
)

// The baseline gate compares each image's old pin with its new pin. These tests
// drive compareAgainst through fakes for its collaborators, so no image is ever
// pulled and no vulnerability database is consulted.

// fakeScan reports, for each image, that many fixable app-level findings.
// Scanning an image it was not told about fails, so a test notices a scan the
// gate should not have made only through its result, never by accident.
func fakeScan(appFixable map[string]int) (scanFunc, *[]string) {
	var scanned []string
	return func(_, img string) (scan.Provenance, []scan.Vuln, error) {
		scanned = append(scanned, img)
		n, ok := appFixable[img]
		if !ok {
			return scan.Provenance{}, nil, fmt.Errorf("unexpected scan of %s", img)
		}
		vs := make([]scan.Vuln, n)
		for i := range vs {
			vs[i] = scan.Vuln{ID: fmt.Sprintf("CVE-%d", i), Fixed: "1.0", Class: "lang-pkgs"}
		}
		return scan.Provenance{}, vs, nil
	}, &scanned
}

func newComparer(old map[string][]string, appFixable map[string]int) (comparer, *[]string) {
	sc, scanned := fakeScan(appFixable)
	return comparer{
		scan: sc,
		imagesAt: func(_, id string) ([]string, error) {
			return old[id], nil
		},
		ceiling: func(string) (int, bool) { return 0, false },
	}, scanned
}

const (
	app1 = "example/app:1@sha256:a1"
	app2 = "example/app:2@sha256:a2"
	db16 = "postgres:16@sha256:d16"
	db17 = "postgres:17@sha256:d17"
)

// (a) A real per-image regression still fails.
func TestCompare_PerImageRegressionFails(t *testing.T) {
	c, _ := newComparer(
		map[string][]string{"t": {app1, db16}},
		map[string]int{app1: 3, app2: 5},
	)
	if got := c.compareAgainst("base", []tile{{id: "t", images: []string{app2, db16}}}); got != 1 {
		t.Fatalf("app 3 → 5 must fail the gate, got exit %d", got)
	}
}

// (b) A duplicated image whose pin worsens still fails.
func TestCompare_DuplicatedImageWorseningFails(t *testing.T) {
	c, scanned := newComparer(
		map[string][]string{"t": {app1, db16}},
		map[string]int{db16: 0, db17: 4},
	)
	got := c.compareAgainst("base", []tile{{id: "t", images: []string{app1, db17, db17}}})
	if got != 1 {
		t.Fatalf("database 0 → 4, run twice, must fail the gate, got exit %d", got)
	}
	if len(*scanned) != 2 {
		t.Fatalf("the duplicate is one image and is compared once, scanned %v", *scanned)
	}
}

// (c) A tile gaining a duplicate of a pin it already runs (this PR's bridges)
// has changed nothing scannable. Before the fix immich, sorted, paired its
// second postgres with immich-server and reported a false ROSE 0 → 60.
func TestCompare_UnchangedDuplicatePasses(t *testing.T) {
	c, scanned := newComparer(
		map[string][]string{"t": {app1, db16, "valkey/valkey:9@sha256:v"}},
		map[string]int{}, // any scan at all fails the fake
	)
	got := c.compareAgainst("base", []tile{{id: "t", images: []string{app1, db16, db16, "valkey/valkey:9@sha256:v"}}})
	if got != 0 {
		t.Fatalf("an added duplicate of an existing pin must pass, got exit %d", got)
	}
	if len(*scanned) != 0 {
		t.Fatalf("nothing changed, so nothing should be scanned, scanned %v", *scanned)
	}
}

// (d) The romm shape: sorted old [mariadb, romm], new [mariadb, mariadb, romm].
// By position the last image was past the end of the old list and skipped, so a
// worse app pin there went unchecked. It must be compared against its old pin.
func TestCompare_LastImageAfterDuplicateIsCompared(t *testing.T) {
	const (
		maria = "mariadb:12.3.3@sha256:m"
		romm1 = "rommapp/romm:5.4.1@sha256:r1"
		romm2 = "rommapp/romm:5.5.0@sha256:r2"
	)
	c, scanned := newComparer(
		map[string][]string{"romm": {maria, romm1}},
		map[string]int{romm1: 12, romm2: 26},
	)
	got := c.compareAgainst("base", []tile{{id: "romm", images: []string{maria, maria, romm2}}})
	if got != 1 {
		t.Fatalf("romm 12 → 26 in the last position must fail the gate, got exit %d", got)
	}
	if fmt.Sprint(*scanned) != fmt.Sprint([]string{romm2, romm1}) {
		t.Fatalf("romm's new pin must be compared with its old pin, scanned %v", *scanned)
	}
}

// A repository the tile never carried is scanned in full and reported, as a new
// tile's images are; it fails only on a defect, not on being new.
func TestCompare_NewRepositoryIsScannedAndReported(t *testing.T) {
	c, scanned := newComparer(
		map[string][]string{"t": {app1}},
		map[string]int{db16: 7},
	)
	got := c.compareAgainst("base", []tile{{id: "t", images: []string{app1, db16}}})
	if got != 0 {
		t.Fatalf("a new repository is reported, not a regression, got exit %d", got)
	}
	if fmt.Sprint(*scanned) != fmt.Sprint([]string{db16}) {
		t.Fatalf("the new repository must be scanned, scanned %v", *scanned)
	}
}

func TestPairImages(t *testing.T) {
	cases := []struct {
		name        string
		old, new    []string
		wantChanges []pinChange
		wantAdded   []string
	}{
		{"re-pin", []string{app1, db16}, []string{app2, db16}, []pinChange{{app1, app2}}, nil},
		{"second tag of a kept repository", []string{db16}, []string{db16, db17}, []pinChange{{db16, db17}}, nil},
		{"new repository", []string{app1}, []string{app1, db16}, nil, []string{db16}},
		{"removal only", []string{app1, db16}, []string{app1}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes, added := pairImages(tc.old, tc.new)
			if fmt.Sprint(changes) != fmt.Sprint(tc.wantChanges) || fmt.Sprint(added) != fmt.Sprint(tc.wantAdded) {
				t.Fatalf("got changes %v added %v, want %v %v", changes, added, tc.wantChanges, tc.wantAdded)
			}
		})
	}
}

func TestRepository(t *testing.T) {
	for in, want := range map[string]string{
		"mariadb:12.3.3@sha256:d":                 "mariadb",
		"ghcr.io/geekdojo/human-system@sha256:7b": "ghcr.io/geekdojo/human-system",
		"localhost:5000/foo:1":                    "localhost:5000/foo",
		"valkey/valkey":                           "valkey/valkey",
	} {
		if got := repository(in); got != want {
			t.Errorf("repository(%q) = %q, want %q", in, got, want)
		}
	}
}
