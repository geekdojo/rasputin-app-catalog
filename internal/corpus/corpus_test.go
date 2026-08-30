package corpus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

const shippedCorpus = "../../tiles"

// The one that matters: the tiles we actually ship must produce a bundle the
// control plane will accept. A unit test over a synthetic corpus proves the
// code works; this proves the CORPUS does, which is the thing a publish
// depends on and the thing a contributor changes.
func TestBuildBundle_ShippedCorpusIsPublishable(t *testing.T) {
	b, err := BuildBundle(shippedCorpus, 1, "2026-08-20T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("the shipped corpus does not build a valid bundle: %v", err)
	}
	if len(b.Tiles) == 0 {
		t.Fatal("built an empty bundle from a non-empty corpus")
	}

	// Round-trip through the reader's own entry point, not just Validate —
	// this is the path a cluster takes, marshalling included.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := tileschema.ParseBundle(raw)
	if err != nil {
		t.Fatalf("a bundle we built was rejected by the reader: %v", err)
	}
	if len(got.Tiles) != len(b.Tiles) {
		t.Errorf("round-trip changed tile count: %d -> %d", len(b.Tiles), len(got.Tiles))
	}
	t.Logf("%d tiles, %d bytes", len(b.Tiles), len(raw))
}

// The artifact is signed over its bytes, so two builds of one corpus must be
// identical. Directory iteration order or map ordering leaking into the output
// would make every rebuild a different signature — and would fail
// intermittently rather than always, which is worse.
func TestBuildBundle_IsDeterministic(t *testing.T) {
	first, err := BuildBundle(shippedCorpus, 7, "2026-08-20T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	a, _ := json.Marshal(first)
	for i := 0; i < 20; i++ {
		next, err := BuildBundle(shippedCorpus, 7, "2026-08-20T00:00:00Z", "test")
		if err != nil {
			t.Fatalf("BuildBundle: %v", err)
		}
		b, _ := json.Marshal(next)
		if string(a) != string(b) {
			t.Fatalf("build %d differs from the first — the bundle is not reproducible", i)
		}
	}
}

// A publisher that can emit something the fleet rejects has moved the failure
// out of CI and into a cluster, with a signature making it look authoritative
// on the way.
func TestBuildBundle_RefusesToEmitAnInvalidBundle(t *testing.T) {
	if _, err := BuildBundle(shippedCorpus, 0, "2026-08-20T00:00:00Z", ""); err == nil {
		t.Fatal("version 0 must be refused at build time, not discovered at the cluster")
	}
}

func TestBuildBundle_CarriesComposeBesideTheTileNotInsideIt(t *testing.T) {
	b, err := BuildBundle(shippedCorpus, 1, "2026-08-20T00:00:00Z", "")
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	var checked int
	for _, bt := range b.Tiles {
		if bt.Tile.ComposeYAML != "" {
			t.Errorf("tile %q leaked compose into the tile object; the wire format carries it beside", bt.Tile.ID)
		}
		if bt.Compose != "" {
			checked++
			if len(bt.Safety.Images) == 0 {
				t.Errorf("tile %q ships a stack but no derived images", bt.Tile.ID)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tile in the corpus ships a compose — this test proved nothing")
	}
}

// A preview tile may ship metadata only. It must build, and it must not
// acquire safety facts invented from nothing.
func TestBuildBundle_PreviewTileWithoutCompose(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "preview-only", `{
      "id":"preview-only","name":"Preview","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"status":"preview","ports":[]}`, "")

	b, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
	if err != nil {
		t.Fatalf("a metadata-only preview tile must build: %v", err)
	}
	if got := b.Tiles[0].Safety; len(got.Images) != 0 {
		t.Errorf("a tile with no stack must have no derived facts, got %#v", got)
	}
}

func TestLoad_Rejects(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "mismatched", `{"id":"something-else","name":"n"}`, "")
	if _, err := Load(root, "mismatched"); err == nil {
		t.Error("an id that disagrees with its directory must be refused")
	}

	writeTile(t, root, "broken", `{not json`, "")
	if _, err := Load(root, "broken"); err == nil {
		t.Error("unparseable tile.json must be refused")
	}

	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "empty"); err == nil {
		t.Error("a directory with no tile.json must be refused")
	}
}

func TestIDs_SortedAndRefusesEmpty(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"zulu", "alpha", "mike"} {
		if err := os.MkdirAll(filepath.Join(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := IDs(root)
	if err != nil {
		t.Fatalf("IDs: %v", err)
	}
	if ids[0] != "alpha" || ids[2] != "zulu" {
		t.Errorf("not sorted: %v", ids)
	}
	if _, err := IDs(t.TempDir()); err == nil {
		t.Error("an empty corpus must be an error, not an empty bundle")
	}
}

func writeTile(t *testing.T, root, id, tileJSON, composeYAML string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TileFile), []byte(tileJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if composeYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, ComposeFile), []byte(composeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Every tile that uses the web-port field must NAME the capability that field
// arrived with, or an older control plane misreads it in the worst possible
// direction (#387/#388).
//
// The rename from `primary` to `web` is not additive, and Decision 7's
// tolerance is what makes that dangerous: a reader that predates it ignores the
// field it does not recognise, finds no primary, and — with the count now
// relaxed to zero-or-one — concludes the app has no web page. Jellyfin would
// load, install, and simply offer no way to open it. Naming tile.web-port turns
// that silence into a refusal with a reason.
//
// The check is on the CORPUS rather than the schema on purpose: the validator
// cannot require a capability (a tile is free not to use the field), so the only
// place "we always declare what we depend on" can be enforced is here, over the
// tiles we actually publish. Verified once by hand against the pre-rename
// tileschema — every migrated tile came back "requires unknown capability
// \"tile.web-port\"" — and this keeps the next tile honest.
func TestShippedCorpus_DeclaresTheWebPortCapability(t *testing.T) {
	ids, err := IDs(shippedCorpus)
	if err != nil {
		t.Fatalf("list corpus: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("empty corpus")
	}
	for _, id := range ids {
		raw, err := os.ReadFile(filepath.Join(shippedCorpus, id, "tile.json"))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		var tile tileschema.Tile
		if err := json.Unmarshal(raw, &tile); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !slices.Contains(tile.Requires, tileschema.CapabilityWebPort) {
			t.Errorf("tile %q does not require %q — an older control plane would read it as page-less instead of refusing it",
				id, tileschema.CapabilityWebPort)
		}
		// And the raw JSON must not still carry the old field: a tile keeping
		// `primary` alongside `web` would validate here and mean something
		// different to every reader.
		if bytes.Contains(raw, []byte(`"primary"`)) {
			t.Errorf("tile %q still carries the retired \"primary\" field", id)
		}
	}
}
