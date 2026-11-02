package legacybridge

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const tilesDir = "../../tiles"

// tokenRE matches a platform secret token in a compose. tileschema owns the
// full grammar; the tiles under test use only the simple form.
var tokenRE = regexp.MustCompile(`\$\{secret:([a-z0-9-]+)\}`)

// readCompose returns the shipped compose of a tile, as bytes on disk.
func readCompose(t *testing.T, tile string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tilesDir, tile, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read the %s compose: %v", tile, err)
	}
	return string(b)
}

// composeFile is the part of a compose these tests read.
type composeFile struct {
	Services map[string]service `yaml:"services"`
}

type service struct {
	Image       string            `yaml:"image"`
	Restart     string            `yaml:"restart"`
	DependsOn   yaml.Node         `yaml:"depends_on"`
	Environment map[string]string `yaml:"environment"`
	Command     []string          `yaml:"command"`
}

func parseCompose(t *testing.T, text string) composeFile {
	t.Helper()
	var c composeFile
	if err := yaml.Unmarshal([]byte(text), &c); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	return c
}

// dependsOn returns each dependency of a service and its condition, reading
// both the list form (condition service_started) and the map form.
func (s service) dependsOn(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	switch s.DependsOn.Kind {
	case 0:
	case yaml.SequenceNode:
		for _, n := range s.DependsOn.Content {
			out[n.Value] = "service_started"
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(s.DependsOn.Content); i += 2 {
			var v struct {
				Condition string `yaml:"condition"`
			}
			if err := s.DependsOn.Content[i+1].Decode(&v); err != nil {
				t.Fatalf("decode depends_on: %v", err)
			}
			out[s.DependsOn.Content[i].Value] = v.Condition
		}
	default:
		t.Fatalf("depends_on has an unexpected shape")
	}
	return out
}

// secretEnvName is the Compose variable the control plane puts in place of a
// token: proto.SecretEnvName in rasputin-control-plane.
func secretEnvName(name string) string {
	return "RASPUTIN_SECRET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// secretNames lists the distinct token names in a compose, in order of first
// appearance.
func secretNames(text string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range tokenRE.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// renderAsDeployed replaces each token with its interpolation reference, as
// the control plane does before the compose reaches an agent.
func renderAsDeployed(text string) string {
	return tokenRE.ReplaceAllStringFunc(text, func(tok string) string {
		return "${" + secretEnvName(tokenRE.FindStringSubmatch(tok)[1]) + "}"
	})
}

// renderLegacy builds the compose a tile shipped before it used the platform's
// secrets: each token is the published literal for its name, and the bridge
// service is gone. The images are the current pins, so the legacy stack moves
// with Renovate's bumps instead of pinning an old image this test would then
// have to pull. Every name in the compose must have a literal.
func renderLegacy(t *testing.T, text, bridge string, literals map[string]string) string {
	t.Helper()
	text = tokenRE.ReplaceAllStringFunc(text, func(tok string) string {
		name := tokenRE.FindStringSubmatch(tok)[1]
		v, ok := literals[name]
		if !ok {
			t.Fatalf("no legacy literal for secret %q", name)
		}
		return v
	})
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	services := doc["services"].(map[string]any)
	delete(services, bridge)
	for _, s := range services {
		svc := s.(map[string]any)
		if deps, ok := svc["depends_on"].(map[string]any); ok {
			delete(deps, bridge)
		}
	}
	return marshal(t, doc)
}

// withoutPorts drops every published port, so a test stack never collides
// with another stack or with anything on the host. The bridge never uses one.
func withoutPorts(t *testing.T, text string) string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	for _, s := range doc["services"].(map[string]any) {
		delete(s.(map[string]any), "ports")
	}
	return marshal(t, doc)
}

func marshal(t *testing.T, doc map[string]any) string {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal compose: %v", err)
	}
	return string(b)
}
