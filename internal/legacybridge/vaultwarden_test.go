package legacybridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TC-552-12: vaultwarden ships with sign-ups closed and its admin token from
// the owner's optional operator secret (Bryce's option (c), F-552-01), with no
// token text in any comment (R7).
func TestVaultwardenTileShape_TC_552_12(t *testing.T) {
	text := readCompose(t, "vaultwarden")
	env := parseCompose(t, text).Services["vaultwarden"].Environment
	if got := env["SIGNUPS_ALLOWED"]; got != "false" {
		t.Errorf("SIGNUPS_ALLOWED = %q, want \"false\"", got)
	}
	if got := env["ADMIN_TOKEN"]; got != "${secret:admin-token}" {
		t.Errorf("ADMIN_TOKEN = %q, want the admin-token operator secret", got)
	}
	for i, line := range strings.Split(text, "\n") {
		if _, comment, ok := strings.Cut(line, "#"); ok && strings.Contains(comment, "${secret:") {
			t.Errorf("line %d carries a token in a comment (R7): %q", i+1, line)
		}
	}

	b, err := os.ReadFile(filepath.Join(tilesDir, "vaultwarden", "tile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tile struct {
		Requires        []string `json:"requires"`
		OperatorSecrets []struct {
			Name     string `json:"name"`
			Label    string `json:"label"`
			Required *bool  `json:"required"`
		} `json:"operatorSecrets"`
	}
	if err := json.Unmarshal(b, &tile); err != nil {
		t.Fatalf("parse tile.json: %v", err)
	}
	for _, want := range []string{"tile.secrets", "tile.operator-secrets"} {
		if !slices.Contains(tile.Requires, want) {
			t.Errorf("requires %v lacks %q", tile.Requires, want)
		}
	}
	if len(tile.OperatorSecrets) != 1 {
		t.Fatalf("want one operator secret, got %+v", tile.OperatorSecrets)
	}
	s := tile.OperatorSecrets[0]
	if s.Name != "admin-token" || strings.TrimSpace(s.Label) == "" {
		t.Errorf("operator secret = %+v, want admin-token with a label", s)
	}
	if s.Required == nil || *s.Required {
		t.Errorf("admin-token must declare required: false, so an upgrade applies without it (option (c))")
	}
}
