package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
	"gopkg.in/yaml.v3"
)

// Literal secrets: the machine half of docs/tile-secrets.md R4.
//
// The catalog is public and every cluster installs the same compose, so a
// password written into it is known to everyone. tileschema cannot refuse it:
// the control plane parses no YAML (ADR-0006 Decision 4), and a rule every tile
// must obey there would refuse the embedded floor, which still carries
// literals. So the check lives here, beside volume coverage, and runs on both
// the lint path (cmd/tilelint) and the publish path (corpus.BuildBundle).
//
// It is deliberately narrow. It reads only `services.*.environment`, only keys
// whose name says they hold a secret, and only non-empty values with no token
// in them. An empty value is how a tile says "off" (human-system's
// HS_MCP_TOKEN), and keys that are not secret-named, `command:` and the like
// stay with reviewers (R4).

// literalExemptions are the (tile, service, key) triples allowed to carry a
// literal: exactly EXCEPTIONS.md EX-1's three bridge inputs (RULING-521-A).
// Keyed on the exact triple, never on a prefix, so the exemption cannot grow
// by naming a new key LEGACY_*. Read-only.
var literalExemptions = map[string]bool{
	"romm/romm-db-bridge/LEGACY_DB_PASSWORD":     true,
	"romm/romm-db-bridge/LEGACY_ROOT_PASSWORD":   true,
	"immich/immich-db-bridge/LEGACY_DB_PASSWORD": true,
}

// secretWords mark a key as secret-named wherever they appear in it.
var secretWords = []string{"PASSWORD", "PASSWD", "PASSPHRASE", "SECRET", "TOKEN"}

// secretSegments mark a key as secret-named only as a whole `_`-separated
// segment: API_KEY and RABBITMQ_DEFAULT_PASS are, KEYBOARD_LAYOUT is not.
var secretSegments = map[string]bool{"KEY": true, "PWD": true, "PASS": true}

// envEntry is one environment variable as the compose spells it. set is false
// when the entry carries no value at all (`KEY:` in a mapping, or a bare `KEY`
// in a list), which Compose reads as "take it from the host", not as a literal.
type envEntry struct {
	key, value string
	set        bool
}

// LiteralSecretProblems reports every non-empty literal value under a
// secret-named key in a tile's `services.*.environment`, except the EX-1
// triples. Each problem is one sentence naming the service and the key, and
// never the value. A compose that does not parse is an error, never a pass.
//
// The compose is decoded into `any` so that YAML anchors, aliases and `<<`
// merge keys are resolved before the environment is read: an environment
// shared through an alias is checked on every service that uses it.
func LiteralSecretProblems(tileID string, yml []byte) ([]string, error) {
	var doc any
	if err := yaml.Unmarshal(yml, &doc); err != nil {
		return nil, fmt.Errorf("parse compose environment: %w", err)
	}
	services := asMap(asMap(doc)["services"])

	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []string
	for _, svc := range names {
		for _, e := range envEntries(asMap(services[svc])["environment"]) {
			if !secretNamed(e.key) || !literal(e) || literalExemptions[tileID+"/"+svc+"/"+e.key] {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"Service %q in docker-compose.yml gives %s a literal value. This file is public and every cluster installs the same one, so a value written here is known to everyone; write %s<name>} instead.",
				svc, e.key, tileschema.SecretTokenPrefix))
		}
	}
	return problems, nil
}

// envEntries reads a service's `environment:` in both Compose spellings: a
// mapping of KEY: value, and a list of "KEY=value" strings. A mapping comes
// back in key order so the problems are stable across runs.
func envEntries(v any) []envEntry {
	var out []envEntry
	switch env := v.(type) {
	case []any:
		for _, item := range env {
			s, ok := item.(string)
			if !ok {
				continue
			}
			key, value, set := strings.Cut(s, "=")
			out = append(out, envEntry{key: strings.TrimSpace(key), value: value, set: set})
		}
	default:
		m := asMap(v)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if m[k] == nil {
				out = append(out, envEntry{key: k})
				continue
			}
			out = append(out, envEntry{key: k, value: fmt.Sprint(m[k]), set: true})
		}
	}
	return out
}

// asMap returns v as a string-keyed mapping, or nil when it is not one. yaml.v3
// decodes a mapping with any non-string key as map[any]any.
func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out
	}
	return nil
}

// secretNamed reports whether an environment key names a secret, ignoring case.
func secretNamed(key string) bool {
	k := strings.ToUpper(key)
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	for _, seg := range strings.Split(k, "_") {
		if secretSegments[seg] {
			return true
		}
	}
	return false
}

// literal reports whether an entry carries a value written into the file: one
// that is present, not blank, and holds no platform token.
func literal(e envEntry) bool {
	return e.set && strings.TrimSpace(e.value) != "" && !strings.Contains(e.value, tileschema.SecretTokenPrefix)
}
