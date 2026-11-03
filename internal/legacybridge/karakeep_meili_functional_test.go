//go:build functional

package legacybridge

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// meiliPrevious is the meilisearch engine karakeep shipped until Renovate's
// bumps to 1.56 (rasputin-app-catalog#78, #81), pinned by digest. An index it
// wrote is the one an owner who installed karakeep then still has.
const meiliPrevious = "getmeili/meilisearch:v1.53.1@sha256:8d6643d86d71fad6ad3cba92cde7ccfce9e4d6c384bda67598eb553571c32431"

// meiliIncompatible starts the line meilisearch logs before it exits on an
// index written by another engine version.
const meiliIncompatible = "Your database version (1.53.1) is incompatible with your current engine version"

// The document the test indexes before the upgrade and searches for after.
const meiliIndex, meiliMarker = "tc-521-22", "upgradeprobe"

// withMeili returns the head compose with the meilisearch service on image
// (the head's own when empty), and with MEILI_UPGRADE_DB removed unless
// upgrade is set.
func withMeili(t *testing.T, head, image string, upgrade bool) string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(head), &doc); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	svc := doc["services"].(map[string]any)["meilisearch"].(map[string]any)
	if image != "" {
		svc["image"] = image
	}
	if !upgrade {
		delete(svc["environment"].(map[string]any), "MEILI_UPGRADE_DB")
	}
	return marshal(t, doc)
}

// meiliCall runs curl inside the meilisearch container against its own API,
// with the container's own master key, so the key is on no command line.
func (s *stack) meiliCall(method, path, body string) (string, error) {
	out, err := exec.Command("docker", "exec", s.containerID("meilisearch"), "sh", "-c",
		`if [ -n "$2" ]; then set -- "$1" --data-binary "$2"; else set -- "$1"; fi
curl -sS --fail-with-body -X "$0" -H "Authorization: Bearer $MEILI_MASTER_KEY" -H "Content-Type: application/json" "$@"`,
		method, "http://127.0.0.1:7700"+path, body).CombinedOutput()
	return string(out), err
}

func (s *stack) mustMeili(method, path, body string) string {
	s.t.Helper()
	out, err := s.meiliCall(method, path, body)
	if err != nil {
		s.t.Fatalf("%s %s: %v: %s", method, path, err, out)
	}
	return out
}

// meiliRestarts returns how often the meilisearch container has restarted.
func (s *stack) meiliRestarts() string {
	s.t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{.RestartCount}}", s.containerID("meilisearch")).Output()
	if err != nil {
		s.t.Fatalf("inspect meilisearch: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// meiliHealthy waits until meilisearch reports itself available, failing at
// once if it restarts instead.
func (s *stack) meiliHealthy() {
	s.t.Helper()
	s.waitFor("meilisearch answering /health", func() bool {
		if n := s.meiliRestarts(); n != "0" {
			s.t.Fatalf("meilisearch restarted %s times instead of answering; its log:\n%s", n, s.logs("meilisearch"))
		}
		out, err := s.meiliCall("GET", "/health", "")
		return err == nil && strings.Contains(out, `"available"`)
	})
}

// meiliVersion returns the engine version meilisearch reports.
func (s *stack) meiliVersion() string {
	s.t.Helper()
	var v struct{ PkgVersion string }
	if out := s.mustMeili("GET", "/version", ""); json.Unmarshal([]byte(out), &v) != nil {
		s.t.Fatalf("decode /version: %s", out)
	}
	return v.PkgVersion
}

// meiliTaskDone waits until every task of the given type succeeded, failing
// at once on a failed or canceled one.
func (s *stack) meiliTaskDone(types string) {
	s.t.Helper()
	s.waitFor("meilisearch "+types+" tasks succeeding", func() bool {
		var r struct {
			Results []struct {
				Status string
				Error  any
			}
		}
		out := s.mustMeili("GET", "/tasks?types="+types, "")
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			s.t.Fatalf("decode /tasks: %v: %s", err, out)
		}
		if len(r.Results) == 0 {
			return false
		}
		for _, task := range r.Results {
			switch task.Status {
			case "failed", "canceled":
				s.t.Fatalf("a %s task ended %s: %v", types, task.Status, task.Error)
			case "succeeded":
			default:
				return false
			}
		}
		return true
	})
}

// meiliFound reports whether a search for the marker finds the document.
func (s *stack) meiliFound() bool {
	s.t.Helper()
	var r struct{ Hits []struct{ ID string } }
	out := s.mustMeili("POST", "/indexes/"+meiliIndex+"/search", `{"q":"`+meiliMarker+`"}`)
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		s.t.Fatalf("decode search: %v: %s", err, out)
	}
	return len(r.Hits) == 1 && r.Hits[0].ID == "1"
}

// meiliStable asserts the meilisearch container is running and has never
// restarted.
func (s *stack) meiliStable() {
	s.t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Status}} {{.RestartCount}}",
		s.containerID("meilisearch")).Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != "running 0" {
		s.t.Errorf("meilisearch is %q (status, restarts; err %v), want running 0", got, err)
	}
}

// meiliWrittenByPrevious brings meilisearch up on the previous engine, indexes
// the marker document and waits until it is searchable.
func meiliWrittenByPrevious(t *testing.T) *stack {
	s := newStack(t, karakeep)
	s.mustUp(withMeili(t, s.head, meiliPrevious, false), "meilisearch")
	s.meiliHealthy()
	if v := s.meiliVersion(); v != "1.53.1" {
		t.Fatalf("the previous engine reports %s, want 1.53.1", v)
	}
	s.mustMeili("POST", "/indexes/"+meiliIndex+"/documents?primaryKey=id",
		`[{"id":"1","title":"`+meiliMarker+` written by 1.53.1"}]`)
	s.meiliTaskDone("documentAdditionOrUpdate")
	if !s.meiliFound() {
		t.Fatal("the document is not found before the upgrade")
	}
	return s
}

// TC-521-22: the head compose brought up on an index written by meilisearch
// 1.53.1 upgrades it in place: meilisearch stays up without a restart and the
// document indexed before the upgrade is still found.
func TestKarakeepMeiliUpgrade(t *testing.T) {
	t.Parallel()
	s := meiliWrittenByPrevious(t)

	s.mustUp(s.head, "meilisearch")
	s.meiliHealthy()
	if v := s.meiliVersion(); v != "1.56.1" {
		t.Errorf("after the upgrade meilisearch reports %s, want the head engine 1.56.1", v)
	}
	s.meiliTaskDone("upgradeDatabase")
	if !s.meiliFound() {
		t.Error("the document indexed before the upgrade is not found after it")
	}
	s.meiliStable()
}

// TC-521-22 negative control: without MEILI_UPGRADE_DB the same upgrade
// refuses the index and crash-loops, which is the defect (F-521-12).
func TestKarakeepMeiliUpgradeWithoutSettingCrashLoops(t *testing.T) {
	t.Parallel()
	s := meiliWrittenByPrevious(t)

	s.mustUp(withMeili(t, s.head, "", false), "meilisearch")
	s.waitFor("meilisearch restarting on the incompatible index", func() bool { return s.meiliRestarts() != "0" })
	logs := s.logs("meilisearch")
	i := strings.Index(logs, meiliIncompatible)
	if i < 0 {
		t.Fatalf("meilisearch did not log %q; its log:\n%s", meiliIncompatible, logs)
	}
	t.Logf("meilisearch restarted %s times, logging: %s", s.meiliRestarts(), strings.SplitN(logs[i:], "\n", 2)[0])
}

// TC-521-22: a fresh install of the head compose on empty volumes starts
// meilisearch healthy; the upgrade setting has nothing to upgrade there.
func TestKarakeepMeiliFresh(t *testing.T) {
	t.Parallel()
	s := newStack(t, karakeep)
	s.mustUp(s.head, "meilisearch")
	s.meiliHealthy()
	s.mustMeili("POST", "/indexes/"+meiliIndex+"/documents?primaryKey=id",
		`[{"id":"1","title":"`+meiliMarker+` written by the head engine"}]`)
	s.meiliTaskDone("documentAdditionOrUpdate")
	if !s.meiliFound() {
		t.Error("a document indexed on a fresh install is not found")
	}
	s.meiliStable()
}
