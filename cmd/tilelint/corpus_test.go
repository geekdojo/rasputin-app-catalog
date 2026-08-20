package main

import (
	"path/filepath"
	"testing"
)

// The shipped corpus must be clean. This is the check that turns the linter
// from a tool someone remembers to run into a gate CI enforces — and it is also
// the first exercise ValidateTileSafety gets against real compose stacks rather
// than hand-written table cases (geekdojo/geekdojo-brain#188).
//
// Offline by construction: no -arch probe here, because a unit test that needs
// two registries is a unit test that fails when Docker Hub has a bad afternoon.
// The architecture claims are verified by the scheduled workflow instead.
func TestShippedCorpusIsClean(t *testing.T) {
	root := filepath.Join("..", "..", "tiles")
	ids, err := tileIDs(root)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("corpus is empty — the linter would trivially pass")
	}

	for _, id := range ids {
		problems, notices := checkTile(root, id, false)
		for _, problem := range problems {
			t.Errorf("%s: %s", id, problem)
		}
		// Notices are not failures. Logging them means a change in what the
		// shipped corpus asks for shows up in the test output rather than
		// only in CI's, so a tile that starts taking privilege is noticed
		// here too (#195).
		for _, n := range notices {
			t.Logf("%s: privilege — %s", id, n)
		}
	}
	t.Logf("%d tiles validated", len(ids))
}
