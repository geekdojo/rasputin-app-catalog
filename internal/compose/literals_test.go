package compose

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// literalsIn runs LiteralSecretProblems and fails the test on a parse error.
func literalsIn(t *testing.T, tileID, yml string) []string {
	t.Helper()
	problems, err := LiteralSecretProblems(tileID, []byte(yml))
	if err != nil {
		t.Fatalf("LiteralSecretProblems: %v", err)
	}
	return problems
}

// oneEnv is a single service "app" whose environment is the given block,
// already indented for the mapping spelling.
func oneEnv(env string) string {
	return "services:\n  app:\n    image: x\n    environment:\n" + env
}

var sentence = regexp.MustCompile(`^[A-Z].*\.$`)

// TC-552-01: a literal in the mapping spelling is refused with one sentence
// that names the service and the key, and never the value.
func TestLiteralSecretProblems_MappingSpelling_TC_552_01(t *testing.T) {
	problems := literalsIn(t, "t", oneEnv("      DB_PASSWORD: hunter2\n"))
	if len(problems) != 1 {
		t.Fatalf("want exactly one problem, got %q", problems)
	}
	p := problems[0]
	for _, want := range []string{`"app"`, "DB_PASSWORD"} {
		if !strings.Contains(p, want) {
			t.Errorf("problem does not name %s: %q", want, p)
		}
	}
	if !sentence.MatchString(p) {
		t.Errorf("problem is not a sentence (capital first, full stop last): %q", p)
	}
	if strings.Contains(p, "hunter2") {
		t.Errorf("problem prints the value: %q", p)
	}
}

// TC-552-02: a literal in the list spelling is refused without the value.
func TestLiteralSecretProblems_ListSpelling_TC_552_02(t *testing.T) {
	problems := literalsIn(t, "t", "services:\n  app:\n    image: x\n    environment: [\"DB_PASSWORD=hunter2\"]\n")
	if len(problems) != 1 || !strings.Contains(problems[0], "DB_PASSWORD") {
		t.Fatalf("want one problem naming DB_PASSWORD, got %q", problems)
	}
	if strings.Contains(problems[0], "hunter2") {
		t.Errorf("problem prints the value: %q", problems[0])
	}
}

// TC-552-03: the key match ignores case.
func TestLiteralSecretProblems_CaseInsensitive_TC_552_03(t *testing.T) {
	if problems := literalsIn(t, "t", oneEnv("      webserver_api_password: changeme\n")); len(problems) != 1 {
		t.Fatalf("want one problem for a lower-case secret key, got %q", problems)
	}
}

// TC-552-04: the key vocabulary, including PASS as a whole segment
// (F-552-04), and keys that only look similar.
func TestLiteralSecretProblems_Vocabulary_TC_552_04(t *testing.T) {
	for _, c := range []struct {
		key     string
		refused bool
	}{
		{"X_PASSWORD", true},
		{"X_PASSWD", true},
		{"X_PASSPHRASE", true},
		{"X_SECRET", true},
		{"X_TOKEN", true},
		{"API_KEY", true},
		{"DB_PWD", true},
		{"RABBITMQ_DEFAULT_PASS", true},
		{"KEYBOARD_LAYOUT", false},
		{"TZ", false},
		{"PUID", false},
	} {
		t.Run(c.key, func(t *testing.T) {
			got := literalsIn(t, "t", oneEnv("      "+c.key+": somevalue\n"))
			if refused := len(got) == 1; refused != c.refused || len(got) > 1 {
				t.Fatalf("%s: refused=%v, want %v (%q)", c.key, refused, c.refused, got)
			}
		})
	}
}

// TC-552-05: tokens, empty and blank values, and entries with no value pass,
// as does a literal under a key that is not secret-named.
func TestLiteralSecretProblems_AllowedValues_TC_552_05(t *testing.T) {
	for name, env := range map[string]string{
		"token":                   "      DB_PASSWORD: \"${secret:db}\"\n",
		"token inside a URL":      "      DATABASE_URL_SECRET: \"postgres://u:${secret:db}@h/d\"\n",
		"empty":                   "      DB_PASSWORD: \"\"\n",
		"blank":                   "      DB_PASSWORD: \"   \"\n",
		"mapping null value":      "      DB_PASSWORD:\n",
		"list entry with no =":    "      - DB_PASSWORD\n",
		"list entry, empty after": "      - DB_PASSWORD=\n",
		"literal under TZ":        "      TZ: Europe/London\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := literalsIn(t, "t", oneEnv(env)); len(got) != 0 {
				t.Fatalf("want no problems, got %q", got)
			}
		})
	}
}

// TC-552-06: anchors, aliases and merge keys are resolved, so a literal
// shared from service a reaches b by alias and c by merge (F-552-03).
func TestLiteralSecretProblems_AnchorsAndMergeKeys_TC_552_06(t *testing.T) {
	const yml = `services:
  a:
    image: x
    environment: &env
      DB_PASSWORD: x
  b:
    image: x
    environment: *env
  c:
    image: x
    environment:
      <<: *env
      TZ: UTC
`
	problems := literalsIn(t, "t", yml)
	if len(problems) != 3 {
		t.Fatalf("want three problems, one per service, got %q", problems)
	}
	for i, svc := range []string{`"a"`, `"b"`, `"c"`} {
		if !strings.Contains(problems[i], svc) {
			t.Errorf("problem %d does not name service %s: %q", i, svc, problems[i])
		}
	}
}

// TC-552-07: the exemption is exactly EX-1's three triples, and nothing near
// them (RULING-521-A).
func TestLiteralSecretProblems_ExemptionIsNarrow_TC_552_07(t *testing.T) {
	if len(literalExemptions) != 3 {
		t.Fatalf("the exemption set holds %d entries, want exactly EX-1's 3", len(literalExemptions))
	}
	for _, c := range []struct {
		tile, svc, key string
		refused        bool
	}{
		{"romm", "romm-db-bridge", "LEGACY_DB_PASSWORD", false},
		{"romm", "romm-db-bridge", "LEGACY_ROOT_PASSWORD", false},
		{"immich", "immich-db-bridge", "LEGACY_DB_PASSWORD", false},
		{"romm", "romm-db", "LEGACY_DB_PASSWORD", true},
		{"karakeep", "karakeep-db-bridge", "LEGACY_DB_PASSWORD", true},
		{"vaultwarden", "vaultwarden", "LEGACY_ADMIN_TOKEN", true},
		{"immich", "immich-server", "LEGACY_DB_PASSWORD", true},
	} {
		t.Run(c.tile+"/"+c.svc+"/"+c.key, func(t *testing.T) {
			yml := "services:\n  " + c.svc + ":\n    image: x\n    environment:\n      " + c.key + ": published\n"
			got := literalsIn(t, c.tile, yml)
			if refused := len(got) == 1; refused != c.refused {
				t.Fatalf("refused=%v, want %v (%q)", refused, c.refused, got)
			}
		})
	}
}

// TC-552-08 (function half): YAML that does not parse is a wrapped error,
// never a pass.
func TestLiteralSecretProblems_UnparseableFailsClosed_TC_552_08(t *testing.T) {
	problems, err := LiteralSecretProblems("t", []byte("services: [\n  app: {\n"))
	if err == nil {
		t.Fatalf("want an error for unparseable YAML, got problems %q and no error", problems)
	}
	if !strings.HasPrefix(err.Error(), "parse compose environment: ") || errors.Unwrap(err) == nil {
		t.Errorf("want a wrapped parse error, got %v", err)
	}
	if problems != nil {
		t.Errorf("an error must come with no problems, got %q", problems)
	}
}
