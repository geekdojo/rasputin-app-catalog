// Package compose derives the security-relevant facts of a tile's compose
// stack, so the control plane never has to.
//
// ADR-0006 Decision 8 puts the safety RULES in the shared tileschema module and
// the FACT EXTRACTION here, on the publisher side. The control plane validates
// the derived facts carried in the signed bundle manifest rather than reparsing
// compose: api/internal/catalog deliberately imports no YAML parser, and
// Decision 4 exists to narrow what sits between an attacker-supplied bundle and
// a cluster. The bundle signature makes these facts exactly as trustworthy as
// the compose they came from.
package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
	"gopkg.in/yaml.v3"
)

// file is the subset of the compose spec that carries safety meaning. Anything
// not named here is irrelevant to the checks and deliberately not modeled —
// this is not a compose implementation.
type file struct {
	Services map[string]service `yaml:"services"`
}

type service struct {
	Image       string   `yaml:"image"`
	Build       any      `yaml:"build"`
	Privileged  bool     `yaml:"privileged"`
	NetworkMode string   `yaml:"network_mode"`
	PID         string   `yaml:"pid"`
	IPC         string   `yaml:"ipc"`
	CapAdd      []string `yaml:"cap_add"`
	Devices     []string `yaml:"devices"`
	Volumes     []any    `yaml:"volumes"`
}

// Extract parses a compose stack into the facts tileschema.ValidateTileSafety
// checks. It reports a parse error, never a policy verdict — deciding what is
// acceptable is the validator's job, and keeping the two apart is what lets the
// same rules run on both sides.
func Extract(yml []byte) (tileschema.SafetyFacts, error) {
	var f file
	if err := yaml.Unmarshal(yml, &f); err != nil {
		return tileschema.SafetyFacts{}, fmt.Errorf("parse compose: %w", err)
	}
	if len(f.Services) == 0 {
		return tileschema.SafetyFacts{}, fmt.Errorf("compose declares no services")
	}

	var out tileschema.SafetyFacts
	caps := map[string]bool{}

	for _, name := range sortedKeys(f.Services) {
		s := f.Services[name]

		// A catalog tile ships a published image. Building from source on the
		// appliance would mean shipping a stack whose contents no digest
		// describes, which is the property every other check here depends on.
		if strings.TrimSpace(s.Image) == "" {
			if s.Build != nil {
				return out, fmt.Errorf("service %q builds from source; catalog tiles must reference a published image", name)
			}
			return out, fmt.Errorf("service %q declares no image", name)
		}
		out.Images = append(out.Images, s.Image)

		if s.Privileged {
			out.Privileged = true
		}
		if strings.EqualFold(s.NetworkMode, "host") {
			out.HostNetwork = true
		}
		if strings.EqualFold(s.PID, "host") || strings.EqualFold(s.IPC, "host") {
			out.HostPIDOrIPC = true
		}
		for _, c := range s.CapAdd {
			caps[strings.ToUpper(strings.TrimSpace(c))] = true
		}
		for _, d := range s.Devices {
			if h := hostSide(d); h != "" {
				out.Devices = append(out.Devices, h)
			}
		}
		for _, v := range s.Volumes {
			if src, ok := bindSource(v); ok {
				out.BindMounts = append(out.BindMounts, src)
			}
		}
	}

	for c := range caps {
		out.CapAdd = append(out.CapAdd, c)
	}
	sort.Strings(out.CapAdd)
	sort.Strings(out.BindMounts)
	sort.Strings(out.Devices)
	return out, nil
}

// bindSource reports the host path of a volume entry, if it is a bind mount.
// Compose allows a short string form and a long mapping form, and only the
// short form is ambiguous: "name:/target" is a named volume while
// "/path:/target" is a bind. The leading character is what separates them.
func bindSource(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		src := t
		if i := strings.Index(t, ":"); i >= 0 {
			src = t[:i]
		}
		if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") ||
			strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~") {
			return src, true
		}
		return "", false // named volume
	case map[string]any:
		if fmt.Sprint(t["type"]) != "bind" {
			return "", false
		}
		if s, ok := t["source"].(string); ok {
			return s, true
		}
	}
	return "", false
}

// hostSide returns the host half of a "host:container[:perms]" device mapping.
func hostSide(d string) string {
	if i := strings.Index(d, ":"); i >= 0 {
		return d[:i]
	}
	return d
}

func sortedKeys(m map[string]service) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
