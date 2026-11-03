package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// TC-552-10: a tile that never touches the secrets channel (no token, no
// tile.secrets, no operatorSecrets) but ships a literal DB_PASSWORD is
// refused, and the refusal links R4 of the guide.
func TestLiteralRefusalLinksR4_TC_552_10(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("literal")
	if slices.Contains(tile.Requires, tileschema.CapabilityTileSecrets) || len(tile.OperatorSecrets) != 0 {
		t.Fatal("fixture must not use the secrets channel")
	}
	composeYAML := secretCompose("hunter2")
	if strings.Contains(composeYAML, tileschema.SecretTokenPrefix) {
		t.Fatal("fixture must carry no token")
	}
	writeTile(t, root, "literal", tile, composeYAML)

	problems, _, _ := checkTile(root, "literal", false)
	if len(problems) != 1 {
		t.Fatalf("want exactly the literal refusal, got %q", problems)
	}
	if !strings.Contains(problems[0], "docs/tile-secrets.md#r4-never-ship-a-literal") {
		t.Fatalf("the refusal does not link R4: %q", problems[0])
	}
	if !strings.Contains(problems[0], "DB_PASSWORD") || strings.Contains(problems[0], "hunter2") {
		t.Fatalf("the refusal must name the key and not the value: %q", problems[0])
	}
}

// TC-552-08 (tilelint half): a compose that does not parse is reported as a
// problem, never passed.
func TestCheckTileReportsUnparseableCompose_TC_552_08(t *testing.T) {
	root := t.TempDir()
	writeTile(t, root, "broken", baseTile("broken"), "services: [\n  app: {\n")

	problems, _, _ := checkTile(root, "broken", false)
	if len(problems) == 0 {
		t.Fatal("an unparseable compose must be reported as a problem")
	}
}
