package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-app-catalog/internal/compose"
	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// writeTile lays out one tile the way corpus.Load expects to find it. Compose
// is omitted entirely when empty, which is what makes a tile PREVIEW-shaped.
func writeTile(t *testing.T, root, id string, tile tileschema.Tile, composeYAML string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(tile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tile.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if composeYAML != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func baseTile(id string) tileschema.Tile {
	return tileschema.Tile{
		ID: id, Name: "Test", Tagline: "A tile", Collection: tileschema.CollectionEveryday,
		Arch: "both", ExposureDefault: "lan-only", RAMFloorMB: 256,
		Ports: []tileschema.Port{{Name: "web", Container: 80, Published: 8080, Primary: true}},
	}
}

// A host-trusting stack whose tile declares nothing. This is the shape #199
// exists to stop: caught at publish rather than at load on somebody's cluster.
const hostTrustingCompose = `services:
  app:
    image: ghcr.io/example/app@` + testDigest + `
    privileged: true
    network_mode: host
`

func TestLint_UnderDeclaredIsAProblemAndPrintsWhatToPaste(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "under", baseTile("under"), hostTrustingCompose)

	problems, _, _ := checkTile(root, "under", false)
	joined := strings.Join(problems, "\n")
	if len(problems) == 0 {
		t.Fatal("an under-declared host-trusting stack must fail the lint")
	}
	for _, want := range []string{
		`takes "host-trusting"`,             // the verdict
		"declare it in tile.json",           // the actionable half
		`"tier": "host-trusting"`,           // the snippet's tier
		`"privileged"`,                      // the derived grant
		tileschema.CapabilityPrivilegeTiers, // the must-understand capability
		"TODO: one line an owner reads",     // the explainer placeholder
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}

// The snippet has to be paste-able, not merely indicative. Marshal it back and
// confirm it produces a declaration the validator then accepts — otherwise the
// helpful message is a second guess at the format it was meant to remove.
func TestLint_SnippetIsPasteableAndSatisfiesTheValidator(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "paste", baseTile("paste"), hostTrustingCompose)

	problems, _, _ := checkTile(root, "paste", false)
	snippet := ""
	for _, p := range problems {
		if i := strings.Index(p, "declare it in tile.json:\n"); i >= 0 {
			snippet = p[i+len("declare it in tile.json:\n"):]
		}
	}
	if snippet == "" {
		t.Fatal("no snippet emitted")
	}

	// Wrap the fragment into an object and decode it as a Tile, exactly as a
	// contributor pasting it into tile.json would.
	var parsed struct {
		Requires  []string             `json:"requires"`
		Privilege tileschema.Privilege `json:"privilege"`
	}
	if err := json.Unmarshal([]byte("{\n"+snippet+"\n}"), &parsed); err != nil {
		t.Fatalf("snippet is not valid JSON: %v\n%s", err, snippet)
	}

	tile := baseTile("paste")
	tile.Requires = parsed.Requires
	tile.Privilege = parsed.Privilege
	tile.Privilege.Why = "because the test says so"
	tile.ComposeYAML = hostTrustingCompose

	facts := extractOrFail(t, hostTrustingCompose)
	if err := tileschema.ValidateTileSafety(tile, facts); err != nil {
		t.Fatalf("the snippet the linter printed does not satisfy the validator: %v", err)
	}
}

// Decision 12c: a consent prompt with no reason is a consent prompt that
// teaches nothing. Enforced by the publisher rather than the reader, because
// it is about what the catalog vouches for.
func TestLint_MissingWhyIsAProblem(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("nowhy")
	tile.Requires = []string{tileschema.CapabilityPrivilegeTiers}
	tile.Privilege = tileschema.Privilege{
		Tier:   tileschema.TierHostTrusting,
		Grants: []string{tileschema.GrantPrivileged, tileschema.GrantHostNetwork},
	}
	writeTile(t, root, "nowhy", tile, hostTrustingCompose)

	problems, _, _ := checkTile(root, "nowhy", false)
	if !strings.Contains(strings.Join(problems, "\n"), "no privilege.why") {
		t.Fatalf("a fully declared tile with no explainer must fail, got %v", problems)
	}

	tile.Privilege.Why = "controls devices on your network"
	writeTile(t, root, "nowhy", tile, hostTrustingCompose)
	if problems, _, _ := checkTile(root, "nowhy", false); len(problems) != 0 {
		t.Fatalf("a fully declared tile with an explainer must be clean, got %v", problems)
	}
}

// Over-declaration is allowed and still worth saying: a badge scarier than the
// stack teaches an owner to click through the next one.
func TestLint_OverDeclarationIsANoticeNotAProblem(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("over")
	tile.Requires = []string{tileschema.CapabilityPrivilegeTiers}
	tile.Privilege = tileschema.Privilege{
		Tier:   tileschema.TierHostTrusting,
		Grants: []string{tileschema.GrantPrivileged},
		Why:    "we would rather over-warn",
	}
	writeTile(t, root, "over", tile, `services:
  app:
    image: ghcr.io/example/app@`+testDigest+`
`)

	problems, notices, _ := checkTile(root, "over", false)
	if len(problems) != 0 {
		t.Fatalf("over-declaration must not fail the lint, got %v", problems)
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "declares privilege it does not take") ||
		!strings.Contains(joined, `declares tier "host-trusting" for a "routine" stack`) {
		t.Fatalf("over-declaration must be reported as a notice, got %v", notices)
	}
}

// #199, the part that is easy to get wrong: a preview tile has no compose, so
// its privilege is UNCOMPUTED. Defaulting it to routine would put a clean
// badge on something nothing has looked at.
func TestLint_PreviewWithoutComposeIsUnverifiedNotRoutine(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("soon")
	tile.Status = tileschema.StatusPreview
	tile.Ports = nil
	writeTile(t, root, "soon", tile, "")

	problems, notices, unverified := checkTile(root, "soon", false)
	if len(problems) != 0 {
		t.Fatalf("a preview tile must lint clean, got %v", problems)
	}
	if !unverified {
		t.Fatal("a preview tile with no compose must be flagged unverified")
	}
	// Undeclared: counted in the run summary, not printed per tile — most of
	// the corpus is preview, and eighteen identical lines is how a reviewer
	// learns to skip the section.
	if len(notices) != 0 {
		t.Fatalf("an undeclared preview tile should be summarised, not annotated: %v", notices)
	}

	// Declared, though, is the interesting case and gets its own line.
	tile.Privilege = tileschema.Privilege{Tier: tileschema.TierElevated, Why: "will need a radio"}
	tile.Requires = []string{tileschema.CapabilityPrivilegeTiers}
	writeTile(t, root, "soon", tile, "")
	_, notices, unverified = checkTile(root, "soon", false)
	if !unverified || len(notices) != 1 || !strings.Contains(notices[0], "no compose to check it against") {
		t.Fatalf("a preview tile that DOES declare privilege must say the declaration is unchecked, got %v", notices)
	}
}

func extractOrFail(t *testing.T, yml string) tileschema.SafetyFacts {
	t.Helper()
	f, err := compose.Extract([]byte(yml))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return f
}
