package legacybridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TC-521-01: each moved tile lists tile.secrets beside its existing
// tile.web-port. The corpus validity and the VERSION raise are the CI tiles
// and version jobs.
func TestMovedTilesRequireTileSecrets(t *testing.T) {
	for _, tile := range []string{"karakeep", "romm", "immich"} {
		b, err := os.ReadFile(filepath.Join(tilesDir, tile, "tile.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Requires []string `json:"requires"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", tile, err)
		}
		for _, want := range []string{"tile.web-port", "tile.secrets"} {
			if !slices.Contains(m.Requires, want) {
				t.Errorf("%s: requires %v lacks %q", tile, m.Requires, want)
			}
		}
	}
}

// TC-521-02: karakeep takes both keys from tokens, one name for the search key
// in both services, and no placeholder or "must be replaced" text remains.
func TestKarakeepKeysAreTokens(t *testing.T) {
	text := readCompose(t, "karakeep")
	c := parseCompose(t, text)
	want := map[string]map[string]string{
		"karakeep": {
			"NEXTAUTH_SECRET":  "${secret:nextauth-secret}",
			"MEILI_MASTER_KEY": "${secret:meili-master-key}",
		},
		"meilisearch": {"MEILI_MASTER_KEY": "${secret:meili-master-key}"},
	}
	for svc, env := range want {
		for k, v := range env {
			if got := c.Services[svc].Environment[k]; got != v {
				t.Errorf("%s %s = %q, want %q", svc, k, got, v)
			}
		}
	}
	assertNoPlaceholderText(t, "karakeep", "change-me", "must be replaced")
}

// TC-521-21 (unit part): karakeep's database is ${DATA_DIR}/db.db, so DATA_DIR
// must be the directory the karakeep-data volume is mounted on (#885).
func TestKarakeepDatabaseInVolume(t *testing.T) {
	k := parseCompose(t, readCompose(t, "karakeep")).Services["karakeep"]
	if got := k.Environment["DATA_DIR"]; got != "/data" {
		t.Errorf("karakeep DATA_DIR = %q, want %q", got, "/data")
	}
	if !slices.Contains(k.Volumes, "karakeep-data:/data") {
		t.Errorf("karakeep volumes %v lack karakeep-data:/data", k.Volumes)
	}
}

// TC-521-03: romm's DB, root and auth secrets are tokens; the published
// passwords appear only as the bridge's LEGACY_* inputs.
func TestRommSecretsAreTokens(t *testing.T) {
	text := readCompose(t, "romm")
	c := parseCompose(t, text)
	want := map[string]map[string]string{
		"romm": {
			"DB_PASSWD":            "${secret:db-password}",
			"ROMM_AUTH_SECRET_KEY": "${secret:auth-secret-key}",
		},
		"romm-db": {
			"MARIADB_PASSWORD":      "${secret:db-password}",
			"MARIADB_ROOT_PASSWORD": "${secret:db-root-password}",
		},
	}
	for svc, env := range want {
		for k, v := range env {
			if got := c.Services[svc].Environment[k]; got != v {
				t.Errorf("%s %s = %q, want %q", svc, k, got, v)
			}
		}
	}
	assertLiteralsOnlyWhereAllowed(t, c, map[string][]string{
		// Identifiers, not secrets.
		"romm/DB_NAME": {"romm"}, "romm/DB_USER": {"romm"},
		"romm-db/MARIADB_DATABASE": {"romm"}, "romm-db/MARIADB_USER": {"romm"},
		// The R4 exception.
		"romm-db-bridge/LEGACY_DB_PASSWORD":   {"romm"},
		"romm-db-bridge/LEGACY_ROOT_PASSWORD": {"romm-root"},
	}, "romm", "romm-root")
	assertNoPlaceholderText(t, "romm", "change-me")
}

// TC-521-04: immich's DB password is one token in both services; the
// published password appears only as the bridge's LEGACY_DB_PASSWORD.
func TestImmichPasswordIsToken(t *testing.T) {
	c := parseCompose(t, readCompose(t, "immich"))
	for svc, k := range map[string]string{"immich-server": "DB_PASSWORD", "database": "POSTGRES_PASSWORD"} {
		if got := c.Services[svc].Environment[k]; got != "${secret:db-password}" {
			t.Errorf("%s %s = %q, want the db-password token", svc, k, got)
		}
	}
	assertLiteralsOnlyWhereAllowed(t, c, map[string][]string{
		"immich-server/DB_USERNAME": {"immich"}, "immich-server/DB_DATABASE_NAME": {"immich"},
		"database/POSTGRES_USER": {"immich"}, "database/POSTGRES_DB": {"immich"},
		"immich-db-bridge/LEGACY_DB_PASSWORD": {"immich"},
	}, "immich")
}

// TC-521-05: the bridge services' shape, R1, R7 and LOG-SECRETS in the compose
// text, and the R4 row in EXCEPTIONS.md.
func TestBridgeShape(t *testing.T) {
	for _, tc := range []struct{ tile, db, bridge, app string }{
		{"romm", "romm-db", "romm-db-bridge", "romm"},
		{"immich", "database", "immich-db-bridge", "immich-server"},
	} {
		t.Run(tc.tile, func(t *testing.T) {
			c := parseCompose(t, readCompose(t, tc.tile))
			db, br, app := c.Services[tc.db], c.Services[tc.bridge], c.Services[tc.app]
			if br.Image == "" || br.Image != db.Image {
				t.Errorf("bridge image %q differs from the database's %q", br.Image, db.Image)
			}
			if br.Restart != "no" {
				t.Errorf("bridge restart = %q, want \"no\"", br.Restart)
			}
			if _, ok := br.dependsOn(t)[tc.db]; !ok {
				t.Errorf("bridge does not depend on %s", tc.db)
			}
			if got := app.dependsOn(t)[tc.bridge]; got != "service_completed_successfully" {
				t.Errorf("%s depends on the bridge with condition %q, want service_completed_successfully", tc.app, got)
			}
			script := strings.Join(br.Command, "\n")
			for _, bad := range []string{"${secret:", "RASPUTIN_SECRET_", "romm-root", "IDENTIFIED BY 'romm'", "PASSWORD 'immich'"} {
				if strings.Contains(script, bad) {
					t.Errorf("bridge command contains %q", bad)
				}
			}
			// Every "$" is the Compose escape "$$", so Compose substitutes
			// nothing into the command and no value can land on it.
			if regexp.MustCompile(`(^|[^$])\$([^$]|$)`).MatchString(strings.ReplaceAll(script, "$$", "")) {
				t.Errorf("bridge command has a \"$\" that is not escaped as \"$$\"")
			}
			// Every upper-case reference is one of the bridge's own variables.
			for _, m := range regexp.MustCompile(`\$\$([A-Z_][A-Z0-9_]*)`).FindAllStringSubmatch(script, -1) {
				if _, ok := br.Environment[m[1]]; !ok {
					t.Errorf("bridge command references $%s, which is not in its environment", m[1])
				}
			}
		})
	}

	commentToken := regexp.MustCompile(`#.*\$\{secret:`)
	for _, tile := range []string{"karakeep", "romm", "immich"} {
		for i, line := range strings.Split(readCompose(t, tile), "\n") {
			if commentToken.MatchString(line) {
				t.Errorf("%s compose line %d writes the token in a comment (R7)", tile, i+1)
			}
		}
	}

	b, err := os.ReadFile("../../EXCEPTIONS.md")
	if err != nil {
		t.Fatalf("EXCEPTIONS.md: %v", err)
	}
	var row string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "| EX-") && strings.Contains(line, "`R4`") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("EXCEPTIONS.md has no R4 row")
	}
	for _, want := range []string{"romm-db-bridge", "immich-db-bridge", "LEGACY_", "true-now",
		"https://github.com/geekdojo/geekdojo-brain/issues/521#issuecomment-"} {
		if !strings.Contains(row, want) {
			t.Errorf("the R4 row lacks %q", want)
		}
	}
	if cells := strings.Split(strings.Trim(row, "|"), "|"); len(cells) != 7 || strings.TrimSpace(cells[5]) == "" {
		t.Errorf("the R4 row needs seven cells with a revisit fact, got %d", len(cells))
	}
}

// assertLiteralsOnlyWhereAllowed fails when any environment value equal to one
// of literals sits anywhere but the allowed service/KEY places.
func assertLiteralsOnlyWhereAllowed(t *testing.T, c composeFile, allowed map[string][]string, literals ...string) {
	t.Helper()
	for name, svc := range c.Services {
		for k, v := range svc.Environment {
			if !slices.Contains(literals, v) {
				continue
			}
			if !slices.Contains(allowed[name+"/"+k], v) {
				t.Errorf("%s %s carries the published value %q", name, k, v)
			}
		}
		for k := range svc.Environment {
			if strings.HasPrefix(k, "LEGACY_") && !strings.HasSuffix(name, "-bridge") {
				t.Errorf("%s carries %s outside a bridge", name, k)
			}
		}
	}
}

func assertNoPlaceholderText(t *testing.T, tile string, needles ...string) {
	t.Helper()
	for _, f := range []string{"docker-compose.yml", "tile.json"} {
		b, err := os.ReadFile(filepath.Join(tilesDir, tile, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				t.Errorf("tiles/%s/%s still contains %q", tile, f, n)
			}
		}
	}
}
