package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
	"gopkg.in/yaml.v3"
)

// secretCompose is a minimal stack whose one service reads value as a
// password. Callers pass a well-formed or a deliberately broken token.
func secretCompose(value string) string {
	return `services:
  app:
    image: ghcr.io/example/app@` + testDigest + `
    environment:
      DB_PASSWORD: "` + value + `"
`
}

func secretTile(id string, requires ...string) tileschema.Tile {
	t := baseTile(id)
	t.Requires = append(baseTile(id).Requires, requires...)
	return t
}

func problemsWithURL(problems []string) int {
	n := 0
	for _, p := range problems {
		if strings.Contains(p, secretsContractURL) {
			n++
		}
	}
	return n
}

// TC-729-01, TC-729-02, TC-729-03, TC-729-04: every secret refusal tilelint
// prints carries the guide's URL.
func TestSecretRefusalsLinkTheGuide(t *testing.T) {
	cases := []struct {
		name    string
		tc      string
		tile    tileschema.Tile
		compose string
	}{
		// TC-729-01: the token is used but tile.secrets is missing from requires.
		{"missing capability", "TC-729-01", secretTile("missing-cap"), secretCompose("${secret:db-password}")},
		// TC-729-02: the escaped form, which a tile must never author.
		{"escaped token", "TC-729-02", secretTile("escaped", tileschema.CapabilityTileSecrets), secretCompose("$${secret:x}")},
		// TC-729-03: a token with no closing brace on its line.
		{"unclosed token", "TC-729-03", secretTile("unclosed", tileschema.CapabilityTileSecrets), secretCompose("${secret:x")},
		// TC-729-04: a name that is not a DNS-1123 label.
		{"invalid name", "TC-729-04", secretTile("bad-name", tileschema.CapabilityTileSecrets), secretCompose("${secret:DB_Pass}")},
	}
	for _, c := range cases {
		t.Run(c.tc+" "+c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTile(t, root, c.tile.ID, c.tile, c.compose)

			problems, _, _ := checkTile(root, c.tile.ID, false)
			if len(problems) == 0 {
				t.Fatalf("%s: expected a refusal, got none", c.tc)
			}
			if problemsWithURL(problems) != 1 {
				t.Fatalf("%s: want exactly one problem carrying %s, got %q", c.tc, secretsContractURL, problems)
			}
		})
	}
}

// TC-729-05: a tile that does not use the secrets channel gets no link, even
// when it is refused.
func TestNonSecretRefusalCarriesNoLink(t *testing.T) {
	root := t.TempDir()
	tile := baseTile("plain")
	tile.Tagline = ""
	writeTile(t, root, "plain", tile, secretCompose("not-a-secret"))

	problems, _, _ := checkTile(root, "plain", false)
	if !strings.Contains(strings.Join(problems, "\n"), "tagline is required") {
		t.Fatalf("want a 'tagline is required' problem, got %q", problems)
	}
	if n := problemsWithURL(problems); n != 0 {
		t.Fatalf("a tile with no secret token must not link the secrets guide; %d problem(s) did: %q", n, problems)
	}
}

// TC-729-15: the link is appended to the existing problem as a sentence on its
// own line; it is not a problem of its own, so the count is unchanged.
func TestSecretsLinkIsAnAppendedSentence(t *testing.T) {
	root := t.TempDir()
	tile := secretTile("missing-cap")
	compose := secretCompose("${secret:db-password}")
	writeTile(t, root, "missing-cap", tile, compose)

	problems, _, _ := checkTile(root, "missing-cap", false)

	// The count without the link: the shared validator's one refusal, and
	// nothing else in this otherwise-valid tile.
	tile.ComposeYAML = compose
	verr := tileschema.ValidateTile(tile)
	if verr == nil {
		t.Fatal("fixture should be refused by ValidateTile")
	}
	if len(problems) != 1 {
		t.Fatalf("the link must not add a problem: want 1, got %d: %q", len(problems), problems)
	}
	head, tail, ok := strings.Cut(problems[0], "\n")
	if !ok {
		t.Fatalf("the link must be on its own line: %q", problems[0])
	}
	if head != verr.Error() {
		t.Fatalf("the existing problem must be unchanged:\n got %q\nwant %q", head, verr.Error())
	}
	sentence := regexp.MustCompile(`^\s+See https://\S+ for how a tile declares its secrets\.$`)
	if !sentence.MatchString(tail) {
		t.Fatalf("appended line is not the expected sentence: %q", tail)
	}
	if !strings.Contains(tail, secretsContractURL) {
		t.Fatalf("appended line does not carry %s: %q", secretsContractURL, tail)
	}
}

// TC-729-15 (CLI half): tilelint exits 1 on the TC-729-01 tile. main calls
// os.Exit, so it runs in a child copy of this test binary.
func TestCLIExitsOneOnSecretRefusal(t *testing.T) {
	if root := os.Getenv("TILELINT_SECRETS_ROOT"); root != "" {
		os.Args = []string{"tilelint", "-root", root, "-tile", "missing-cap"}
		main()
		return // main exits 0 when there are no problems; the parent sees that.
	}

	root := t.TempDir()
	writeTile(t, root, "missing-cap", secretTile("missing-cap"), secretCompose("${secret:db-password}"))

	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIExitsOneOnSecretRefusal$")
	cmd.Env = append(os.Environ(), "TILELINT_SECRETS_ROOT="+root)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), secretsContractURL) {
		t.Fatalf("CLI output does not carry %s:\n%s", secretsContractURL, out)
	}
}

// guidePath turns secretsContractURL into the guide's path in this repository,
// so the URL the linter prints and the file the tests read cannot disagree.
func guidePath(url string) (string, error) {
	const marker = "/blob/main/"
	_, rel, ok := strings.Cut(url, marker)
	if !ok || rel == "" || !strings.HasPrefix(url, "https://github.com/geekdojo/rasputin-app-catalog/") {
		return "", fmt.Errorf("cannot derive a repository path from %q", url)
	}
	return filepath.Join("..", "..", filepath.FromSlash(rel)), nil
}

// exampleBlock returns the fenced block that follows the marker for file.
func exampleBlock(guide, file string) (string, error) {
	marker := "<!-- tilelint-example: " + file + " -->"
	_, after, ok := strings.Cut(guide, marker)
	if !ok {
		return "", fmt.Errorf("guide has no %q marker", marker)
	}
	after = strings.TrimLeft(after, " \t\r\n")
	if !strings.HasPrefix(after, "```") {
		return "", fmt.Errorf("marker %q is not followed by a fenced block", marker)
	}
	_, body, ok := strings.Cut(after, "\n") // drop the opening fence line
	if !ok {
		return "", fmt.Errorf("block after %q is never closed", marker)
	}
	end := strings.Index(body, "\n```")
	if end < 0 {
		return "", fmt.Errorf("block after %q is never closed", marker)
	}
	block := body[:end+1]
	if strings.TrimSpace(block) == "" {
		return "", fmt.Errorf("block after %q is empty", marker)
	}
	return block, nil
}

// guideExample reads the guide at path and returns its worked example.
func guideExample(path string) (tileJSON, composeYAML string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read the secrets guide: %w", err)
	}
	if tileJSON, err = exampleBlock(string(raw), "tile.json"); err != nil {
		return "", "", err
	}
	if composeYAML, err = exampleBlock(string(raw), "docker-compose.yml"); err != nil {
		return "", "", err
	}
	return tileJSON, composeYAML, nil
}

// loadGuideExample returns the guide's worked example, failing (never
// skipping) when the guide or either block cannot be found.
func loadGuideExample(t *testing.T) (tileschema.Tile, string) {
	t.Helper()
	path, err := guidePath(secretsContractURL)
	if err != nil {
		t.Fatal(err)
	}
	tileJSON, composeYAML, err := guideExample(path)
	if err != nil {
		t.Fatal(err)
	}
	var tile tileschema.Tile
	if err := json.Unmarshal([]byte(tileJSON), &tile); err != nil {
		t.Fatalf("the guide's tile.json example does not parse: %v", err)
	}
	return tile, composeYAML
}

// TC-729-06: the guide's worked example is a valid tile, and it shares one
// secret between two services (F-729-02).
func TestGuideExampleIsAValidTile(t *testing.T) {
	tile, composeYAML := loadGuideExample(t)

	var stack struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeYAML), &stack); err != nil {
		t.Fatalf("the guide's compose example does not parse: %v", err)
	}
	services := 0
	for _, svc := range stack.Services {
		for _, v := range svc.Environment {
			if v == "${secret:db-password}" {
				services++
				break
			}
		}
	}
	if services < 2 {
		t.Fatalf("the example must share ${secret:db-password} across two services; found it in %d", services)
	}

	root := t.TempDir()
	writeTile(t, root, tile.ID, tile, composeYAML)
	problems, _, _ := checkTile(root, tile.ID, false)
	if len(problems) != 0 {
		t.Fatalf("the guide's worked example must lint clean, got: %q", problems)
	}
}

// TC-729-08: the guide's own example, with tile.secrets removed, is refused
// with the guide's URL.
func TestGuideExampleWithoutCapabilityIsRefusedWithLink(t *testing.T) {
	tile, composeYAML := loadGuideExample(t)
	var kept []string
	for _, r := range tile.Requires {
		if r != tileschema.CapabilityTileSecrets {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(tile.Requires) {
		t.Fatal("the guide's example does not list tile.secrets, so there is nothing to remove")
	}
	tile.Requires = kept

	root := t.TempDir()
	writeTile(t, root, tile.ID, tile, composeYAML)
	problems, _, _ := checkTile(root, tile.ID, false)
	if problemsWithURL(problems) != 1 {
		t.Fatalf("want the refusal to carry %s, got %q", secretsContractURL, problems)
	}
}

// TC-729-07: a guide that is missing, moved or malformed fails the tests that
// read it. Each path below is one loadGuideExample turns into t.Fatal; none
// skips.
func TestGuideExampleFailurePaths(t *testing.T) {
	const good = "<!-- tilelint-example: tile.json -->\n```json\n{}\n```\n" +
		"<!-- tilelint-example: docker-compose.yml -->\n```yaml\nservices: {}\n```\n"
	write := func(t *testing.T, body string) string {
		p := filepath.Join(t.TempDir(), "tile-secrets.md")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, _, err := guideExample(write(t, good)); err != nil {
		t.Fatalf("control: a well-formed guide must load: %v", err)
	}
	cases := map[string]string{
		"missing file":         filepath.Join(t.TempDir(), "moved.md"),
		"missing tile.json":    write(t, strings.Replace(good, "tile.json -->", "x -->", 1)),
		"missing compose":      write(t, strings.Replace(good, "docker-compose.yml -->", "x -->", 1)),
		"empty block":          write(t, strings.Replace(good, "{}", "", 1)),
		"marker without fence": write(t, strings.Replace(good, "```json", "text", 1)),
		"block never closed":   write(t, strings.TrimSuffix(good, "```\n")),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := guideExample(path); err == nil {
				t.Fatalf("%s: want an error, got none", name)
			}
		})
	}

	for _, url := range []string{
		"https://example.com/docs/tile-secrets.md",
		"https://github.com/geekdojo/rasputin-app-catalog/tree/main/docs/tile-secrets.md",
		"https://github.com/geekdojo/rasputin-app-catalog/blob/main/",
	} {
		if _, err := guidePath(url); err == nil {
			t.Fatalf("unparseable URL %q: want an error, got none", url)
		}
	}
	if _, err := guidePath(secretsContractURL); err != nil {
		t.Fatalf("control: the real URL must parse: %v", err)
	}
}
