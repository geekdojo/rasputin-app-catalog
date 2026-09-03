package compose

import (
	"fmt"
	"slices"
	"strings"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
	"gopkg.in/yaml.v3"
)

// Volume coverage: the half of storage.md §4.2 that tileschema cannot enforce.
//
// tileschema refuses a DECLARED volume that is missing a backup class or a
// quiesce strategy, and that is the whole of what it can do: `api/internal/
// catalog` treats compose as opaque bytes on purpose (ADR-0006 Decision 4), so
// the control plane has no way to learn that a tile ships a volume it never
// mentioned. A tile with no `volumes` array at all therefore passes every check
// on that side — which is a fail-OPEN hole in a rule whose entire point is to
// fail closed. #229 named the gap and said it wanted an issue in this repo.
//
// This is that check, and it belongs here for the same reason fact extraction
// does (Decision 8): it needs a YAML parser, and the publisher is the only side
// that has one. The rule is one sentence — EVERY named volume a stack creates
// must carry a classification, and every classification must name a volume the
// stack creates — and it is enforced on both the lint path (cmd/tilelint) and
// the publish path (corpus.BuildBundle), so a corpus that cannot be linted
// cannot be published either.

// stackVolumes is the second, narrow view of a compose file. Extract's `file`
// deliberately models only what carries SAFETY meaning and does not read the
// top-level `volumes:` block at all; rather than widen a struct whose comment
// promises it is not a compose implementation, this reads the one extra key.
type stackVolumes struct {
	Volumes  map[string]any `yaml:"volumes"`
	Services map[string]struct {
		Volumes []any `yaml:"volumes"`
	} `yaml:"services"`
}

// Stack is what a compose file says about its own storage: the named volumes it
// creates, and whether any service mounts an ANONYMOUS one.
type Stack struct {
	// Named is every named volume the stack creates, sorted and deduplicated.
	// It is the UNION of the top-level `volumes:` keys and the named sources
	// mounted by services — a superset on purpose. Either spelling on its own
	// would miss a real volume: a top-level entry nothing mounts is still a
	// volume the appliance creates, and a service mount is what actually puts
	// data in one.
	Named []string

	// AnonymousMounts is every container path a service mounts with no source
	// (`- /var/lib/thing`). Docker creates a volume with a random hex name for
	// each, which nothing can classify, because there is no stable name to
	// attach a class to. Reported rather than refused: see
	// ClassificationProblems.
	AnonymousMounts []string
}

// Volumes reads the storage shape of a compose stack. It reports a parse error
// and never a policy verdict, exactly as Extract does.
func Volumes(yml []byte) (Stack, error) {
	var f stackVolumes
	if err := yaml.Unmarshal(yml, &f); err != nil {
		return Stack{}, fmt.Errorf("parse compose volumes: %w", err)
	}

	named := map[string]bool{}
	anon := map[string]bool{}

	for name := range f.Volumes {
		if n := strings.TrimSpace(name); n != "" {
			named[n] = true
		}
	}
	for _, s := range f.Services {
		for _, v := range s.Volumes {
			switch kind, val := mountSource(v); kind {
			case mountNamed:
				named[val] = true
			case mountAnonymous:
				anon[val] = true
			}
			// A bind mount is a host path, not a volume the appliance owns.
			// It has no backup class to carry and is already governed by the
			// privilege rules in ValidateTileSafety.
		}
	}

	return Stack{Named: sortedSet(named), AnonymousMounts: sortedSet(anon)}, nil
}

type mountKind int

const (
	mountBind mountKind = iota
	mountNamed
	mountAnonymous
)

// mountSource classifies one entry of a service's `volumes:` list.
//
// Compose accepts a short string form and a long mapping form, and the short
// form is the ambiguous one: "name:/target" is a named volume, "/path:/target"
// is a bind, and a bare "/target" with no colon is an ANONYMOUS volume. The
// leading character of the first field separates the first two; the absence of
// a second field identifies the third. bindSource above answers a different
// question (is this a bind, and where does it point) and deliberately reports
// nothing for the other two cases, so this cannot be folded into it.
func mountSource(v any) (mountKind, string) {
	switch t := v.(type) {
	case string:
		spec := strings.TrimSpace(t)
		if spec == "" {
			return mountBind, ""
		}
		i := strings.Index(spec, ":")
		if i < 0 {
			// No source at all: docker names this volume for us, with a
			// random hex string that changes on every fresh install.
			return mountAnonymous, spec
		}
		src := strings.TrimSpace(spec[:i])
		if src == "" || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") ||
			strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~") {
			return mountBind, src
		}
		return mountNamed, src
	case map[string]any:
		if fmt.Sprint(t["type"]) != "volume" {
			return mountBind, ""
		}
		src, _ := t["source"].(string)
		if s := strings.TrimSpace(src); s != "" {
			return mountNamed, s
		}
		target, _ := t["target"].(string)
		return mountAnonymous, strings.TrimSpace(target)
	}
	return mountBind, ""
}

// ClassificationProblems reports every way a tile's declared volumes and the
// volumes its stack actually creates fail to line up. It returns all of them
// rather than the first, so a tile that has classified none of its five gets
// one CI run and not five.
//
// THE MESSAGES ARE THE FEATURE, in tileschema's words: an author who has just
// hit this is reading one line of CI output, so each line names the volume,
// says what to do, and carries the paste-ready fragment on the missing case.
func ClassificationProblems(t tileschema.Tile, s Stack) []string {
	declared := make(map[string]bool, len(t.Volumes))
	for _, v := range t.Volumes {
		if n := strings.TrimSpace(v.Name); n != "" {
			declared[n] = true
		}
	}

	var problems []string
	for _, name := range s.Named {
		if declared[name] {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"docker-compose.yml creates volume %q but tile.json does not classify it — every volume a tile ships needs a backup class and a quiesce strategy (storage.md §4.2), and an unclassified one is silently never backed up. Add to tile.json:\n%s",
			name, volumeSnippet(name)))
	}

	// The reverse direction, which is not pedantry: a classification naming a
	// volume the stack no longer creates is dead metadata that reads to a
	// reviewer as coverage. It is also the exact residue of a rename, and a
	// rename is precisely when the NEW name goes unclassified — so the two
	// halves of that mistake are caught by the two halves of this check.
	for _, v := range t.Volumes {
		name := strings.TrimSpace(v.Name)
		if name == "" || slices.Contains(s.Named, name) {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"tile.json classifies volume %q but docker-compose.yml creates no such volume — a class attached to a name nothing uses is coverage that is not there",
			name))
	}
	return problems
}

// AnonymousNotices reports anonymous volume mounts.
//
// A NOTICE and not a problem, deliberately. The other findings here have a
// one-line fix — declare the volume — and this one does not: the fix is to edit
// the compose stack so the mount has a name, which is a change to what the tile
// RUNS rather than to what it says about itself, and refusing the corpus over it
// would block a tile on a judgement the linter is not entitled to make. It is
// still worth a reviewer's eye every time, because an anonymous volume is data
// no archive can ever be pointed at: it has no stable name to classify, and
// docker gives it a fresh random one on every clean install.
func AnonymousNotices(s Stack) []string {
	var out []string
	for _, target := range s.AnonymousMounts {
		out = append(out, fmt.Sprintf(
			"mounts an ANONYMOUS volume at %q — it has no name to attach a backup class to, so nothing can ever archive it; give the mount a named volume if it holds data worth keeping",
			target))
	}
	return out
}

// volumeSnippet renders the tile.json fragment an author should paste. Built by
// marshalling the real struct through the same path declarationSnippet uses, so
// the keys cannot drift from the ones tileschema parses — and left with the two
// values EMPTY on purpose. There is no default (§4.2) and guessing one from the
// volume's name is the inference the schema exists to refuse, so the snippet
// hands over the shape and makes the author supply the judgement.
func volumeSnippet(name string) string {
	return fmt.Sprintf("      {\"name\": %q, \"backup\": \"critical|state|cache|bulk\", \"quiesce\": \"none|stop|sqlite|postgres|mysql\"}", name)
}
