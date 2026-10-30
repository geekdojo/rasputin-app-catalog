package corpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

const shippedCorpus = "../../tiles"

// The one that matters: the tiles we actually ship must produce a bundle the
// control plane will accept. A unit test over a synthetic corpus proves the
// code works; this proves the CORPUS does, which is the thing a publish
// depends on and the thing a contributor changes.
//
// TC-899-03: the bundle now publishes each author's tile.json bytes rather
// than the struct, so this also proves a reader on the pinned contract decodes
// exactly the values the publisher validated, tile by tile.
func TestBuildBundle_ShippedCorpusIsPublishable(t *testing.T) {
	raw := encodeCorpus(t, shippedCorpus, 1)

	// Round-trip through the reader's own entry point, not just Validate —
	// this is the path a cluster takes, over the real artifact bytes.
	got, err := tileschema.ParseBundle(raw)
	if err != nil {
		t.Fatalf("a bundle we built was rejected by the reader: %v", err)
	}
	ids, err := IDs(shippedCorpus)
	if err != nil {
		t.Fatalf("IDs: %v", err)
	}
	if len(got.Tiles) != len(ids) {
		t.Fatalf("reader decoded %d tiles from a corpus of %d", len(got.Tiles), len(ids))
	}
	for i, id := range ids {
		l, err := Load(shippedCorpus, id)
		if err != nil {
			t.Fatalf("Load %q: %v", id, err)
		}
		want := l.Tile
		want.ComposeYAML = ""
		if !reflect.DeepEqual(got.Tiles[i].Tile, want) {
			t.Errorf("tile %q: the reader decodes a different tile than the publisher validated\n got: %#v\nwant: %#v", id, got.Tiles[i].Tile, want)
		}
		if got.Tiles[i].Compose != l.Compose {
			t.Errorf("tile %q: the reader decodes a different compose than the publisher read", id)
		}
	}
	t.Logf("%d tiles, %d bytes", len(got.Tiles), len(raw))
}

// The artifact is signed over its bytes, so two builds of one corpus must be
// identical. Directory iteration order or map ordering leaking into the output
// would make every rebuild a different signature — and would fail
// intermittently rather than always, which is worse.
//
// TC-899-04: compared on Encode output, the bytes that get signed.
func TestBuildBundle_IsDeterministic(t *testing.T) {
	first := encodeCorpus(t, shippedCorpus, 7)
	for i := 0; i < 20; i++ {
		if next := encodeCorpus(t, shippedCorpus, 7); !bytes.Equal(first, next) {
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

// TC-899-05: the wire shape existing readers see is unchanged — envelope and
// per-tile key order, compose beside the tile and never inside it, and one
// trailing newline.
func TestBuildBundle_CarriesComposeBesideTheTileNotInsideIt(t *testing.T) {
	raw := encodeCorpus(t, shippedCorpus, 1)

	if !bytes.HasSuffix(raw, []byte("}\n")) || bytes.HasSuffix(raw, []byte("\n\n")) {
		t.Errorf("catalog.json must end in exactly one newline, ends in %q", raw[max(0, len(raw)-3):])
	}

	envelope := objectKeys(t, raw)
	if want := []string{"schemaVersion", "version", "publishedAt", "source", "tiles"}; !slices.Equal(envelope.keys, want) {
		t.Errorf("envelope keys = %v, want %v", envelope.keys, want)
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(envelope.values["tiles"], &entries); err != nil {
		t.Fatalf("tiles: %v", err)
	}
	var checked int
	for i, entry := range entries {
		fields := objectKeys(t, entry)
		if want := []string{"tile", "compose", "safety"}; !slices.Equal(fields.keys, want) {
			t.Errorf("tiles[%d] keys = %v, want %v", i, fields.keys, want)
		}
		tile := objectKeys(t, fields.values["tile"])
		for _, leaked := range []string{"compose", "composeYAML"} {
			if _, ok := tile.values[leaked]; ok {
				t.Errorf("tiles[%d] leaked %q into the tile object; the wire format carries it beside", i, leaked)
			}
		}
		var bt tileschema.BundleTile
		if err := json.Unmarshal(entry, &bt); err != nil {
			t.Fatalf("tiles[%d]: %v", i, err)
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
//
// TC-899-08: on the publish types, with a field the module does not define.
func TestBuildBundle_PreviewTileWithoutCompose(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "preview-only", `{
      "id":"preview-only","name":"Preview","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"status":"preview","ports":[],"zzNotInTileschema":true}`, "")

	b, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
	if err != nil {
		t.Fatalf("a metadata-only preview tile must build: %v", err)
	}
	if got := b.Tiles[0].Safety; len(got.Images) != 0 {
		t.Errorf("a tile with no stack must have no derived facts, got %#v", got)
	}
	raw, err := b.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := tileschema.ParseBundle(raw); err != nil {
		t.Fatalf("the reader rejected a preview bundle: %v", err)
	}
	var generic struct {
		Tiles []struct {
			Tile    map[string]any `json:"tile"`
			Compose *string        `json:"compose"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := generic.Tiles[0].Tile["zzNotInTileschema"]; got != true {
		t.Errorf("the unknown field was not published, got %#v", got)
	}
	if c := generic.Tiles[0].Compose; c == nil || *c != "" {
		t.Errorf(`a preview tile must publish compose as "", got %v`, c)
	}
}

// validStack is a minimal compose a tile with one web port and no volumes can
// publish.
const validStack = `services:
  app:
    image: ghcr.io/example/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// TC-899-01: the issue's "done". A field the pinned tileschema does not define
// reaches catalog.json intact — top level, nested, and inside a known array
// entry — with the author's key order and number literal, because the bytes
// are published and never round-tripped through the struct. Alien names are
// used on purpose: the module will never learn them, so the test keeps
// proving this after a pin bump.
func TestBuildBundle_PublishesFieldsTheSchemaDoesNotDefine(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "carries-unknowns", `{"id":"carries-unknowns","name":"U","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"requires":["tile.web-port"],
      "ports":[{"name":"web","container":80,"published":8080,"web":true,"zzPortExtra":1.0e3}],
      "zzNotInTileschema":false,
      "zzNested":{"z":1,"a":2}}`, validStack)

	raw := encodeCorpus(t, root, 1)
	if _, err := tileschema.ParseBundle(raw); err != nil {
		t.Fatalf("the reader rejected a bundle carrying unknown fields: %v", err)
	}

	var generic struct {
		Tiles []struct {
			Tile struct {
				NotInTileschema *bool            `json:"zzNotInTileschema"`
				Nested          map[string]any   `json:"zzNested"`
				Ports           []map[string]any `json:"ports"`
			} `json:"tile"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tile := generic.Tiles[0].Tile
	if tile.NotInTileschema == nil || *tile.NotInTileschema {
		t.Errorf("zzNotInTileschema did not survive as false: %v", tile.NotInTileschema)
	}
	if tile.Nested == nil {
		t.Error("zzNested did not survive")
	}
	if len(tile.Ports) != 1 {
		t.Fatalf("ports did not survive: %v", tile.Ports)
	}
	if _, ok := tile.Ports[0]["zzPortExtra"]; !ok {
		t.Error("the unknown key inside ports[0] did not survive")
	}

	nested := bytes.Index(raw, []byte(`"zzNested"`))
	if nested < 0 {
		t.Fatal("zzNested missing from the raw bytes")
	}
	z := bytes.Index(raw[nested:], []byte(`"z"`))
	a := bytes.Index(raw[nested:], []byte(`"a"`))
	if z < 0 || a < 0 || z > a {
		t.Errorf("zzNested keys were reordered; the author wrote z before a:\n%s", raw[nested:])
	}
	if !bytes.Contains(raw, []byte(`"zzPortExtra": 1.0e3`)) {
		t.Error("the number literal 1.0e3 was rewritten; it must be published as written")
	}
}

// TC-899-02: the output is a function of the tile's content, not its
// formatting. Two corpora that differ only in whitespace and line endings
// sign identically, and no CR reaches the artifact.
func TestBuildBundle_WhitespaceDoesNotChangeTheOutput(t *testing.T) {
	const compact = `{"id":"ws","name":"W","tagline":"t","description":"d","collection":"essentials","arch":"both","exposureDefault":"lan-only","ramFloorMB":256,"requires":["tile.web-port"],"ports":[{"name":"web","container":80,"published":8080,"web":true}]}`
	const loose = "{\r\n\t\"id\" :  \"ws\",\r\n    \"name\":\"W\", \"tagline\":\"t\",\"description\":\"d\",\r\n" +
		"  \"collection\":\"essentials\",\"arch\":\"both\",\"exposureDefault\":\"lan-only\",\r\n" +
		"        \"ramFloorMB\": 256, \"requires\": [ \"tile.web-port\" ],\r\n" +
		"  \"ports\": [\r\n    { \"name\": \"web\", \"container\": 80, \"published\": 8080, \"web\": true }\r\n  ]\r\n}\r\n"

	a, b := t.TempDir(), t.TempDir()
	writeTile(t, a, "ws", compact, validStack)
	writeTile(t, b, "ws", loose, validStack)

	first, second := encodeCorpus(t, a, 3), encodeCorpus(t, b, 3)
	if !bytes.Equal(first, second) {
		t.Errorf("whitespace changed the published bytes:\n%s\n---\n%s", first, second)
	}
	for _, out := range [][]byte{first, second} {
		if bytes.ContainsRune(out, '\r') {
			t.Error("a carriage return reached catalog.json")
		}
	}
}

// TC-899-06: validation still judges the decoded struct. An unknown field
// does not mask a tile that fails tileschema.ValidateTile, and nothing is
// returned to encode.
func TestBuildBundle_UnknownFieldDoesNotBypassValidation(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "invalid-exposure", `{"id":"invalid-exposure","name":"I","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"not-a-value",
      "ramFloorMB":256,"requires":["tile.web-port"],
      "ports":[{"name":"web","container":80,"published":8080,"web":true}],
      "zzNotInTileschema":false}`, validStack)

	b, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
	if err == nil {
		t.Fatal("a tile that fails validation must be refused even when it carries an unknown field")
	}
	for _, want := range []string{"invalid-exposure", "exposureDefault"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if b.Tiles != nil {
		t.Errorf("a refused build returned %d tiles to publish", len(b.Tiles))
	}
}

// TC-899-07: bytes that are not exactly one JSON object never reach the
// bundle — the decode that precedes publishing refuses trailing content.
func TestBuildBundle_RefusesTrailingContentAfterTheTile(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "trailing", `{"id":"trailing","name":"T","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"requires":["tile.web-port"],
      "ports":[{"name":"web","container":80,"published":8080,"web":true}]}
{"x":1}`, validStack)

	b, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
	if err == nil {
		t.Fatal("a tile.json carrying a second JSON value must be refused")
	}
	for _, want := range []string{"trailing", "parse tile.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if b.Tiles != nil {
		t.Errorf("a refused build returned %d tiles to publish", len(b.Tiles))
	}
}

// A corpus the builder cannot list publishes nothing: a missing tiles root is
// refused with the read error, not turned into an empty bundle.
func TestBuildBundle_RefusesAnUnreadableCorpus(t *testing.T) {
	b, err := BuildBundle(filepath.Join(t.TempDir(), "no-such-tiles"), 1, "2026-08-20T00:00:00Z", "")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing tiles root must be refused with its read error, got %v", err)
	}
	if b.Tiles != nil {
		t.Errorf("a refused build returned %d tiles to publish", len(b.Tiles))
	}
}

// A stack the builder cannot turn into safety facts or a volume inventory is
// refused, naming the tile, and nothing is returned to encode. The two stacks
// fail at different readers: the first declares a service with no image
// (compose.Extract), the second spells the top-level volumes block as a list,
// which Extract does not read and compose.Volumes cannot decode.
func TestBuildBundle_RefusesAStackItCannotRead(t *testing.T) {
	const tileJSON = `{"id":"%s","name":"S","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"requires":["tile.web-port"],
      "ports":[{"name":"web","container":80,"published":8080,"web":true}]}`
	for _, tc := range []struct {
		id, stack, want string
	}{
		{"no-image", "services:\n  app:\n    restart: always\n", `service "app" declares no image`},
		{"volumes-as-list", validStack + "volumes:\n  - data\n", "parse compose volumes"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			writeTile(t, root, tc.id, fmt.Sprintf(tileJSON, tc.id), tc.stack)

			b, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
			if err == nil {
				t.Fatal("a stack the builder cannot read must be refused")
			}
			for _, want := range []string{fmt.Sprintf("tile %q", tc.id), tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q: %v", want, err)
				}
			}
			if b.Tiles != nil {
				t.Errorf("a refused build returned %d tiles to publish", len(b.Tiles))
			}
		})
	}
}

// encodeCorpus builds and encodes root with fixed metadata, failing the test
// on any error.
func encodeCorpus(t *testing.T, root string, version int) []byte {
	t.Helper()
	b, err := BuildBundle(root, version, "2026-08-20T00:00:00Z", "test")
	if err != nil {
		t.Fatalf("BuildBundle: %v", err)
	}
	raw, err := b.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return raw
}

// object is one JSON object's members, with the order they appear in, which
// a map decode alone would lose.
type object struct {
	keys   []string
	values map[string]json.RawMessage
}

func objectKeys(t *testing.T, raw []byte) object {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %v %v", tok, err)
	}
	o := object{values: map[string]json.RawMessage{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		key := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("value of %q: %v", key, err)
		}
		o.keys = append(o.keys, key)
		o.values[key] = v
	}
	return o
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

// The publish path refuses an unclassified corpus, not only the linter.
//
// b.Validate() at the end of BuildBundle catches a DECLARED volume with a
// missing class, but it cannot catch a volume the stack creates and the tile
// never named: the control plane parses no compose (ADR-0006 Decision 4), so
// that fact never reaches it. If the coverage check lived only in cmd/tilelint,
// a dispatch that skipped the lint could still sign and ship the corpus — and
// the signature would make the hole look authoritative on the way out.
func TestBuildBundle_RefusesAnUnclassifiedVolume(t *testing.T) {
	const stack = `services:
  app:
    image: ghcr.io/example/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    volumes:
      - app-data:/data
volumes:
  app-data:
`
	const meta = `{"id":"unclassified","name":"U","tagline":"t","description":"d",
      "collection":"essentials","arch":"both","exposureDefault":"lan-only",
      "ramFloorMB":256,"requires":["tile.web-port"],
      "ports":[{"name":"web","container":80,"published":8080,"web":true}]%s}`

	root := t.TempDir()
	writeTile(t, root, "unclassified", fmt.Sprintf(meta, ""), stack)
	_, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", "")
	if err == nil {
		t.Fatal("a corpus with an unclassified volume must not build a publishable bundle")
	}
	for _, want := range []string{"unclassified", "app-data", "refusing to publish"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}

	// And the same corpus builds once the volume is classified — otherwise
	// this test would pass for any reason at all.
	classified := `,"volumes":[{"name":"app-data","backup":"state","quiesce":"stop"}]`
	writeTile(t, root, "unclassified", fmt.Sprintf(meta, classified), stack)
	if _, err := BuildBundle(root, 1, "2026-08-20T00:00:00Z", ""); err != nil {
		t.Fatalf("the same corpus must build once the volume carries a class: %v", err)
	}
}
