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

	// #195: keys that grant privilege and were previously unread. The types
	// here are wider than they look because compose accepts more than one
	// shape for several of them — sysctls is a map or a list, group_add may
	// hold ints, tmpfs is a scalar or a list. Modelling the narrow shape would
	// silently drop the other one, which is the bug being fixed.
	SecurityOpt  []string       `yaml:"security_opt"`
	UsernsMode   string         `yaml:"userns_mode"`
	GroupAdd     []any          `yaml:"group_add"`
	Sysctls      any            `yaml:"sysctls"`
	VolumesFrom  []string       `yaml:"volumes_from"`
	CgroupParent string         `yaml:"cgroup_parent"`
	Tmpfs        any            `yaml:"tmpfs"`
	Ulimits      map[string]any `yaml:"ulimits"`
	Deploy       *deploy        `yaml:"deploy"`
}

// deploy carries only the reservations branch. GPUs are requested through
// deploy.resources.reservations.devices rather than the top-level `devices:`
// key, so a stack can reach hardware while declaring neither devices nor
// needsHardware — the check in ValidateTileSafety that keeps that field honest
// never fires.
type deploy struct {
	Resources struct {
		Reservations struct {
			Devices []reservedDevice `yaml:"devices"`
		} `yaml:"reservations"`
	} `yaml:"resources"`
}

type reservedDevice struct {
	Driver       string   `yaml:"driver"`
	Capabilities []string `yaml:"capabilities"`
	DeviceIDs    []string `yaml:"device_ids"`
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
	securityOpt := map[string]bool{}
	usernsMode := map[string]bool{}
	groupAdd := map[string]bool{}
	sysctls := map[string]bool{}
	volumesFrom := map[string]bool{}
	cgroupParent := map[string]bool{}
	tmpfs := map[string]bool{}
	ulimits := map[string]bool{}
	reservedDevices := map[string]bool{}
	namespaceJoins := map[string]bool{}

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

		// --- #195 ---
		for _, o := range s.SecurityOpt {
			if n := normalizeOpt(o); n != "" {
				securityOpt[n] = true
			}
		}
		if v := strings.TrimSpace(s.UsernsMode); v != "" {
			usernsMode[v] = true
		}
		for _, g := range s.GroupAdd {
			if v := strings.TrimSpace(fmt.Sprint(g)); v != "" {
				groupAdd[v] = true
			}
		}
		for _, kv := range flattenKV(s.Sysctls) {
			sysctls[kv] = true
		}
		for _, vf := range s.VolumesFrom {
			if v := strings.TrimSpace(vf); v != "" {
				volumesFrom[v] = true
			}
		}
		if v := strings.TrimSpace(s.CgroupParent); v != "" {
			cgroupParent[v] = true
		}
		for _, m := range flattenScalars(s.Tmpfs) {
			tmpfs[m] = true
		}
		for name, lim := range s.Ulimits {
			ulimits[strings.TrimSpace(name)+"="+strings.TrimSpace(fmt.Sprint(lim))] = true
		}
		if s.Deploy != nil {
			for _, d := range s.Deploy.Resources.Reservations.Devices {
				reservedDevices[describeReservation(d)] = true
			}
		}
		// A namespace a service JOINS rather than creates. `service:` names a
		// sibling in this stack — the ordinary VPN-sidecar shape, and something
		// the signed bundle fully describes. `container:` names something
		// outside the bundle entirely, which it does not.
		for kind, val := range map[string]string{"network": s.NetworkMode, "pid": s.PID, "ipc": s.IPC} {
			if j := namespaceJoin(kind, val); j != "" {
				namespaceJoins[j] = true
			}
		}
	}

	for c := range caps {
		out.CapAdd = append(out.CapAdd, c)
	}
	// Every list is sorted. These facts are covered by the bundle signature, so
	// a set iterated in Go's random map order would produce a different
	// manifest for identical input and make a publish unreproducible.
	sort.Strings(out.CapAdd)
	sort.Strings(out.BindMounts)
	sort.Strings(out.Devices)

	out.SecurityOpt = sortedSet(securityOpt)
	out.UsernsMode = sortedSet(usernsMode)
	out.GroupAdd = sortedSet(groupAdd)
	out.Sysctls = sortedSet(sysctls)
	out.VolumesFrom = sortedSet(volumesFrom)
	out.CgroupParent = sortedSet(cgroupParent)
	out.Tmpfs = sortedSet(tmpfs)
	out.Ulimits = sortedSet(ulimits)
	out.ReservedDevices = sortedSet(reservedDevices)
	out.NamespaceJoins = sortedSet(namespaceJoins)
	return out, nil
}

// sortedSet turns a presence set into the deterministic slice the manifest
// needs, and nil when empty so the field stays omitempty.
func sortedSet(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeOpt renders a security_opt entry as "key=value". Compose accepts
// both separators — `seccomp=unconfined` and `seccomp:unconfined` mean the same
// thing — and a value may itself contain a colon (`label=user:USER`), so the
// split is on whichever separator appears FIRST and nothing after it.
func normalizeOpt(o string) string {
	o = strings.TrimSpace(o)
	if o == "" {
		return ""
	}
	i := strings.IndexAny(o, "=:")
	if i < 0 {
		return o
	}
	return strings.TrimSpace(o[:i]) + "=" + strings.TrimSpace(o[i+1:])
}

// namespaceJoin reports "<kind>:<value>" when a service joins an existing
// namespace instead of creating one. "host" is deliberately not reported here —
// HostNetwork and HostPIDOrIPC already carry it, and duplicating a fact under
// two names invites a validator that checks one and not the other.
func namespaceJoin(kind, val string) string {
	val = strings.TrimSpace(val)
	if val == "" || strings.EqualFold(val, "host") {
		return ""
	}
	if strings.HasPrefix(val, "service:") || strings.HasPrefix(val, "container:") {
		return kind + ":" + val
	}
	return ""
}

// describeReservation renders a deploy reservation compactly and stably.
func describeReservation(d reservedDevice) string {
	driver := strings.TrimSpace(d.Driver)
	if driver == "" {
		driver = "any"
	}
	parts := []string{"driver=" + driver}
	if len(d.Capabilities) > 0 {
		caps := append([]string(nil), d.Capabilities...)
		sort.Strings(caps)
		parts = append(parts, "capabilities="+strings.Join(caps, "+"))
	}
	if len(d.DeviceIDs) > 0 {
		ids := append([]string(nil), d.DeviceIDs...)
		sort.Strings(ids)
		parts = append(parts, "ids="+strings.Join(ids, "+"))
	}
	return strings.Join(parts, ",")
}

// flattenKV reads compose's two spellings of a key/value block — a mapping
// (`sysctls: {net.ipv4.ip_forward: 1}`) and a list (`sysctls: ["net.ipv4.ip_forward=1"]`)
// — into "name=value" strings. Reading only one spelling is exactly the class
// of blindness #195 is fixing.
func flattenKV(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			out = append(out, strings.TrimSpace(k)+"="+strings.TrimSpace(fmt.Sprint(val)))
		}
	case map[any]any:
		for k, val := range t {
			out = append(out, strings.TrimSpace(fmt.Sprint(k))+"="+strings.TrimSpace(fmt.Sprint(val)))
		}
	case []any:
		for _, e := range t {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// flattenScalars reads a field compose allows as either one scalar or a list.
func flattenScalars(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	case []any:
		for _, e := range t {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
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
