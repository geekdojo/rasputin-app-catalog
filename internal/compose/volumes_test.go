package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

func TestVolumes_UnionOfTopLevelAndServiceMounts(t *testing.T) {
	// The union is the point. `declared-only` is listed at the top level and
	// mounted by nobody; `mounted-only` is mounted by a service and listed
	// nowhere. Reading either spelling alone would miss a real volume, and the
	// one it missed would be the one nothing forces an author to classify.
	s, err := Volumes([]byte(`
services:
  app:
    image: ` + pinned + `
    volumes:
      - mounted-only:/a
      - both:/b
      - /host/path:/c
      - ./rel:/d
volumes:
  declared-only:
  both:
`))
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	want := []string{"both", "declared-only", "mounted-only"}
	if !reflect.DeepEqual(s.Named, want) {
		t.Errorf("named = %v, want %v (sorted, deduplicated, no bind mounts)", s.Named, want)
	}
	if len(s.AnonymousMounts) != 0 {
		t.Errorf("anonymous = %v, want none", s.AnonymousMounts)
	}
}

func TestVolumes_LongFormAndAnonymous(t *testing.T) {
	// The long form names its own kind, so there is no guessing to do — but a
	// `type: volume` with no source is still anonymous, and a bare container
	// path in the short form is the shape that most often hides one.
	s, err := Volumes([]byte(`
services:
  app:
    image: ` + pinned + `
    volumes:
      - type: volume
        source: long-named
        target: /a
      - type: bind
        source: /etc/shadow
        target: /b
      - type: volume
        target: /anon-long
      - /anon-short
`))
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if !reflect.DeepEqual(s.Named, []string{"long-named"}) {
		t.Errorf("named = %v, want only the long-form named volume", s.Named)
	}
	if !reflect.DeepEqual(s.AnonymousMounts, []string{"/anon-long", "/anon-short"}) {
		t.Errorf("anonymous = %v, want both sourceless mounts", s.AnonymousMounts)
	}
	if notices := AnonymousNotices(s); len(notices) != 2 {
		t.Errorf("notices = %v, want one per anonymous mount", notices)
	}
}

func TestVolumes_NamesWithUnderscoresSurvive(t *testing.T) {
	// tileschema deliberately does not constrain a volume name to a DNS label
	// because compose names legally carry underscores. A check that silently
	// dropped them would leave exactly those volumes unclassifiable.
	s, err := Volumes([]byte(`
services:
  app:
    image: ` + pinned + `
    volumes:
      - my_app_data:/data
`))
	if err != nil {
		t.Fatalf("Volumes: %v", err)
	}
	if !reflect.DeepEqual(s.Named, []string{"my_app_data"}) {
		t.Errorf("named = %v, want the underscored name intact", s.Named)
	}
}

func TestClassificationProblems(t *testing.T) {
	classified := []tileschema.Volume{
		{Name: "data", Backup: tileschema.BackupState, Quiesce: tileschema.QuiesceStop},
	}

	cases := []struct {
		name     string
		tile     tileschema.Tile
		stack    Stack
		want     int
		contains []string
	}{{
		name:  "fully classified",
		tile:  tileschema.Tile{Volumes: classified},
		stack: Stack{Named: []string{"data"}},
		want:  0,
	}, {
		// The case this check exists for, and the one tileschema cannot see:
		// no `volumes` array at all is not an empty tile, it is an unanswered
		// question, and every volume in the stack is silently unarchived.
		name:     "declares nothing at all",
		tile:     tileschema.Tile{},
		stack:    Stack{Named: []string{"cache", "data"}},
		want:     2,
		contains: []string{`"cache"`, `"data"`, "storage.md §4.2"},
	}, {
		name:     "one of three missed",
		tile:     tileschema.Tile{Volumes: classified},
		stack:    Stack{Named: []string{"data", "media"}},
		want:     1,
		contains: []string{`"media"`},
	}, {
		name:     "classifies a volume the stack does not create",
		tile:     tileschema.Tile{Volumes: classified},
		stack:    Stack{Named: []string{"renamed-data"}},
		want:     2, // the new name unclassified AND the old class orphaned
		contains: []string{`"renamed-data"`, `"data"`, "coverage that is not there"},
	}, {
		// A bind mount is a host path the appliance does not own. It carries
		// no class, and demanding one would refuse every correct tile.
		name:  "bind mounts are not volumes",
		tile:  tileschema.Tile{Volumes: classified},
		stack: Stack{Named: []string{"data"}, AnonymousMounts: []string{"/tmp/x"}},
		want:  0,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassificationProblems(tc.tile, tc.stack)
			if len(got) != tc.want {
				t.Fatalf("got %d problem(s), want %d: %v", len(got), tc.want, got)
			}
			joined := strings.Join(got, "\n")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Errorf("message does not mention %s:\n%s", want, joined)
				}
			}
		})
	}
}

// The snippet a failing author pastes must actually parse as the field
// tileschema reads, and must NOT arrive pre-filled. A snippet carrying a
// plausible-looking default would reintroduce by suggestion exactly the default
// §4.2 refuses to have in the schema — the author would paste it and move on.
func TestVolumeSnippetOffersTheVocabularyAndNotAnAnswer(t *testing.T) {
	snippet := volumeSnippet("some-data")
	for _, want := range []string{`"name": "some-data"`, "critical|state|cache|bulk", "none|stop|sqlite|postgres|mysql"} {
		if !strings.Contains(snippet, want) {
			t.Errorf("snippet is missing %q:\n%s", want, snippet)
		}
	}
	for _, class := range tileschema.BackupClasses {
		if strings.Contains(snippet, `"backup": "`+class+`"`) {
			t.Errorf("snippet pre-answers backup as %q — that is the default §4.2 refuses", class)
		}
	}
}
