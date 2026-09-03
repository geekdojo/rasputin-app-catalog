package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-app-catalog/internal/corpus"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// A stack with two volumes: one the author remembered and one they did not.
const twoVolumeCompose = `services:
  app:
    image: ghcr.io/example/app@` + testDigest + `
    volumes:
      - app-data:/data
      - app-cache:/cache
volumes:
  app-data:
  app-cache:
`

// THE GATE, seen failing. A tile that classifies one of its two volumes must
// fail the lint, and the failure must name the volume it missed WITHOUT
// implicating the one it got right — a tile author reads one line of CI output,
// and a message that lists everything tells them nothing.
//
// This is the half tileschema cannot reach. Its validators refuse a DECLARED
// volume with a missing class, but the control plane parses no compose
// (ADR-0006 Decision 4), so a volume the stack creates and the tile never
// mentions is invisible there. #229 shipped the contract and named this gap;
// this is the check that closes it, on the only side that can.
func TestLint_UnclassifiedVolumeFailsAndNamesTheRightOne(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("half")
	tile.Volumes = []tileschema.Volume{
		{Name: "app-data", Backup: tileschema.BackupState, Quiesce: tileschema.QuiesceStop},
	}
	writeTile(t, root, "half", tile, twoVolumeCompose)

	problems, _, _ := checkTile(root, "half", false)
	if len(problems) == 0 {
		t.Fatal("a stack with an unclassified volume must fail the lint")
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		`"app-cache"`,                  // WHICH volume
		"does not classify it",         // what is wrong
		"storage.md §4.2",              // where the rule lives
		"never backed up",              // what it costs
		`"backup": "critical|state|`,   // the paste-able shape
		`"quiesce": "none|stop|sqlite`, // both fields, both vocabularies
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, `creates volume "app-data"`) {
		t.Errorf("the message implicates a volume that IS classified:\n%s", joined)
	}
}

// The whole point of the rule is that it fires on a tile carrying no volumes
// array at all, which is precisely the state every tile in this repo was in
// before #293. Absent is not "nothing to declare" — it is unanswered.
func TestLint_ATileThatDeclaresNoVolumesAtAllStillFails(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "silent", baseTile("silent"), twoVolumeCompose)

	problems, _, _ := checkTile(root, "silent", false)
	if len(problems) != 2 {
		t.Fatalf("want one problem per unclassified volume, got %d: %v", len(problems), problems)
	}
}

// A preview tile ships no compose, so there is nothing to check coverage
// against. It must not be failed for a stack it does not have — and equally
// must not be reported as covered.
func TestLint_PreviewTileIsNotFailedForVolumeCoverage(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("preview")
	tile.Status = "preview"
	writeTile(t, root, "preview", tile, "")

	problems, _, unverified := checkTile(root, "preview", false)
	if !unverified {
		t.Fatal("a tile with no compose is unverified")
	}
	for _, p := range problems {
		if strings.Contains(p, "classify") {
			t.Errorf("a preview tile was failed for volume coverage: %s", p)
		}
	}
}

// An anonymous mount is a NOTICE and not a problem: there is no declaration the
// author could write that would fix it, because docker gives the volume a fresh
// random name on every install. It still has to be said out loud every time,
// since it is data no archive can ever be pointed at.
func TestLint_AnonymousVolumeIsANoticeNotAProblem(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "anon", baseTile("anon"), `services:
  app:
    image: ghcr.io/example/app@`+testDigest+`
    volumes:
      - /var/lib/scratch
`)

	problems, notices, _ := checkTile(root, "anon", false)
	for _, p := range problems {
		if strings.Contains(p, "classify") {
			t.Errorf("an anonymous mount must not fail the VOLUME check: %s", p)
		}
	}
	if !strings.Contains(strings.Join(notices, "\n"), "ANONYMOUS") {
		t.Errorf("an anonymous mount must be reported: %v", notices)
	}

	// Documenting a pre-existing misreading rather than silently working
	// around it: Extract's bindSource treats a sourceless `- /path` as a BIND
	// MOUNT of that host path, so this tile also draws a spurious privilege
	// escalation. It errs toward over-declaring, which is the safe direction,
	// and correcting it changes what DerivePrivilege returns for a shape no
	// shipped tile uses — a privilege change, not a backup one, and not this
	// PR's to make. Pinned here so the day it is fixed, this says so.
	if !strings.Contains(strings.Join(problems, "\n"), "bind:/var/lib/scratch") {
		t.Log("bindSource no longer misreads an anonymous mount as a bind — good; drop this note")
	}
}

// Coverage of the SHIPPED corpus, asserted as a number rather than left to
// TestShippedCorpusIsClean's silence. That test would still pass if a future
// edit deleted a tile's volumes array AND its compose volumes together; this
// one says how much classified storage the catalog is expected to describe, so
// a drop shows up as a diff to a number a reviewer has to agree with.
func TestShippedCorpusClassifiesEveryVolume(t *testing.T) {
	const wantVolumes = 35

	root := filepath.Join("..", "..", "tiles")
	ids, err := corpus.IDs(root)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}

	classified := 0
	byClass := map[string]int{}
	for _, id := range ids {
		l, err := corpus.Load(root, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if l.HasCompose && len(l.Tile.Volumes) == 0 {
			t.Errorf("%s: ships a stack but classifies no volumes", id)
		}
		for _, v := range l.Tile.Volumes {
			classified++
			byClass[v.Backup]++
		}
	}
	if classified != wantVolumes {
		t.Errorf("corpus classifies %d volumes, expected %d — if this is a deliberate change, update the constant in the same PR", classified, wantVolumes)
	}

	// Every class must actually be in use. A vocabulary where one value never
	// appears is a value nobody has had to think about, and `critical` is the
	// one that matters: if it ever reads zero, the password vault has been
	// quietly demoted.
	for _, class := range tileschema.BackupClasses {
		if byClass[class] == 0 {
			t.Errorf("no volume in the shipped corpus is classified %q", class)
		}
	}
	t.Logf("%d volumes classified: %v", classified, byClass)
}
