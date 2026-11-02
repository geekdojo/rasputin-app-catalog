//go:build functional

package legacybridge

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The sentences the bridges print. Each test matches the start of one, so a
// test proves which branch ran, not only the exit code.
const (
	sayAlready     = "The database already uses the platform's password"
	sayMoved       = "Moved the database from the published legacy password"
	sayRefusedBoth = "The database refused both the platform's and the published legacy"
	sayNoAnswer    = "The database did not answer within 120 seconds"
)

// readinessBound is the bridges' wait for their database. The agent's default
// deploy budget is 300 seconds (rasputin-control-plane proto.AppDeployWork),
// and neither tile declares deployBudgetSeconds, so the bound must stay below.
const (
	readinessBound = 120 * time.Second
	deployBudget   = 300 * time.Second
)

// waitBound bounds every wait the test itself makes for a fact: a database
// accepting a login, or an app logging that it reached its database.
const waitBound = 4 * time.Minute

// bridgeTile describes one tile under test.
type bridgeTile struct {
	tile, db, bridge, app string
	// upTargets are the services `up` is given; empty means all. immich's
	// machine-learning service has no part in the bridge and is a large pull.
	upTargets []string
	// legacy maps each secret name to the literal the tile published before.
	legacy map[string]string
	// appReady is what the app logs once it has its database, and
	// appRefused what it logs when the database refuses its password.
	appReady, appRefused *regexp.Regexp
	// accounts are the database logins the bridge must move, each with the
	// secret whose platform value it takes.
	accounts []account
}

type account struct {
	name   string // for messages
	secret string // the token name whose value this account takes
	local  bool   // a socket login inside the database container
}

var romm = bridgeTile{
	tile: "romm", db: "romm-db", bridge: "romm-db-bridge", app: "romm",
	legacy: map[string]string{
		"db-password":      "romm",
		"db-root-password": "romm-root",
		"auth-secret-key":  "change-me-on-first-run",
	},
	appReady:   regexp.MustCompile(`Database migrations succeeded`),
	appRefused: regexp.MustCompile(`Access denied for user 'romm'`),
	accounts: []account{
		{name: "romm", secret: "db-password"},
		{name: "root", secret: "db-root-password"},
		{name: "root", secret: "db-root-password", local: true},
	},
}

var immich = bridgeTile{
	tile: "immich", db: "database", bridge: "immich-db-bridge", app: "immich-server",
	upTargets:  []string{"immich-server"},
	legacy:     map[string]string{"db-password": "immich"},
	appReady:   regexp.MustCompile(`Finished running migrations`),
	appRefused: regexp.MustCompile(`password authentication failed for user "immich"`),
	accounts:   []account{{name: "immich", secret: "db-password"}},
}

// stack is one throwaway Compose project for one tile.
type stack struct {
	t       *testing.T
	bt      bridgeTile
	project string
	file    string
	head    string // the shipped compose as the control plane deploys it
	values  map[string]string
}

func newStack(t *testing.T, bt bridgeTile) *stack {
	t.Helper()
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		t.Fatalf("this test needs a running Docker daemon: %v: %s", err, out)
	}
	text := readCompose(t, bt.tile)
	s := &stack{
		t: t, bt: bt,
		project: "legacybridge-" + bt.tile + "-" + randomHex(t),
		file:    filepath.Join(t.TempDir(), "docker-compose.yml"),
		head:    withoutPorts(t, renderAsDeployed(text)),
		values:  map[string]string{},
	}
	for _, name := range secretNames(text) {
		s.values[name] = randomValue(t)
	}
	t.Cleanup(func() {
		// Compose finds the project's containers, networks and volumes by
		// label, so this works whichever compose was applied last.
		if out, err := exec.Command("docker", "compose", "-p", s.project, "down", "-v", "--remove-orphans").CombinedOutput(); err != nil {
			t.Errorf("clean up %s: %v: %s", s.project, err, out)
		}
	})
	t.Cleanup(func() { s.t = t; s.assertNoValueLeaked() })
	return s
}

// randomValue has the platform's shape: 32 random bytes, base64url, no
// padding, 43 characters.
func randomValue(t *testing.T) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHex(t *testing.T) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// secretEnv is the environment the agent gives `docker compose up`: one
// RASPUTIN_SECRET_* variable per token.
func (s *stack) secretEnv() []string {
	env := os.Environ()
	for name, v := range s.values {
		env = append(env, secretEnvName(name)+"="+v)
	}
	return env
}

func (s *stack) write(compose string) {
	s.t.Helper()
	if err := os.WriteFile(s.file, []byte(compose), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// compose runs docker compose on the project. withSecrets is false for stop
// and start, which the agent runs without the values.
func (s *stack) compose(withSecrets bool, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose", "-p", s.project, "-f", s.file}, args...)...)
	if withSecrets {
		cmd.Env = s.secretEnv()
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// up applies compose the way the agent deploys.
func (s *stack) up(compose string, targets ...string) (string, error) {
	s.write(compose)
	args := append([]string{"up", "-d", "--remove-orphans", "--quiet-pull"}, targets...)
	return s.compose(true, args...)
}

func (s *stack) mustUp(compose string, targets ...string) {
	s.t.Helper()
	if out, err := s.up(compose, targets...); err != nil {
		s.t.Fatalf("docker compose up: %v\n%s", err, out)
	}
}

func (s *stack) upHead() { s.t.Helper(); s.mustUp(s.head, s.bt.upTargets...) }

// legacyCompose is the compose the tile shipped before, with literals.
func (s *stack) legacyCompose(literals map[string]string) string {
	return withoutPorts(s.t, renderLegacy(s.t, readCompose(s.t, s.bt.tile), s.bt.bridge, literals))
}

type containerState struct {
	Path   string
	Args   []string
	Config struct {
		Cmd        []string
		Entrypoint []string
	}
	State struct {
		Status     string
		Running    bool
		ExitCode   int
		StartedAt  time.Time
		FinishedAt time.Time
	}
}

func (s *stack) containerID(svc string) string {
	s.t.Helper()
	out, err := exec.Command("docker", "ps", "-a", "-q",
		"--filter", "label=com.docker.compose.project="+s.project,
		"--filter", "label=com.docker.compose.service="+svc).Output()
	if err != nil {
		s.t.Fatalf("find the %s container: %v", svc, err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" || strings.Contains(id, "\n") {
		s.t.Fatalf("want one %s container, got %q", svc, id)
	}
	return id
}

func (s *stack) inspect(svc string) containerState {
	s.t.Helper()
	out, err := exec.Command("docker", "inspect", s.containerID(svc)).Output()
	if err != nil {
		s.t.Fatalf("inspect %s: %v", svc, err)
	}
	var cs []containerState
	if err := json.Unmarshal(out, &cs); err != nil || len(cs) != 1 {
		s.t.Fatalf("decode inspect of %s: %v", svc, err)
	}
	return cs[0]
}

func (s *stack) logs(svc string) string {
	s.t.Helper()
	out, err := exec.Command("docker", "logs", s.containerID(svc)).CombinedOutput()
	if err != nil {
		s.t.Fatalf("logs of %s: %v", svc, err)
	}
	return string(out)
}

// bridgeRan asserts the bridge's latest run exited with code and printed want
// as its last line.
func (s *stack) bridgeRan(code int, want string) containerState {
	s.t.Helper()
	st := s.inspect(s.bt.bridge)
	if st.State.Running || st.State.ExitCode != code {
		s.t.Errorf("%s: running=%v exit=%d, want exited %d", s.bt.bridge, st.State.Running, st.State.ExitCode, code)
	}
	lines := strings.Split(strings.TrimSpace(s.logs(s.bt.bridge)), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, want) {
		s.t.Errorf("%s last logged %q, want a line starting %q", s.bt.bridge, last, want)
	} else if !regexp.MustCompile(`^[A-Z].*\.$`).MatchString(last) {
		s.t.Errorf("%s logged %q, which is not a capitalised sentence ending in a full stop", s.bt.bridge, last)
	}
	return st
}

// countLines is how many times the bridge has printed a line starting want.
func (s *stack) countLines(want string) int {
	n := 0
	for _, l := range strings.Split(s.logs(s.bt.bridge), "\n") {
		if strings.HasPrefix(l, want) {
			n++
		}
	}
	return n
}

// waitFor polls a fact until it holds, failing after waitBound.
func (s *stack) waitFor(what string, fact func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(waitBound)
	for !fact() {
		if time.Now().After(deadline) {
			s.t.Fatalf("%s did not happen within %s", what, waitBound)
		}
		time.Sleep(time.Second)
	}
}

// appLogged waits until the app, since its latest start, logs a line
// matching re.
func (s *stack) appLogged(what string, re *regexp.Regexp) {
	s.t.Helper()
	s.waitFor(s.bt.app+" "+what, func() bool {
		since := s.inspect(s.bt.app).State.StartedAt.Format(time.RFC3339Nano)
		out, err := exec.Command("docker", "logs", "--since", since, s.containerID(s.bt.app)).CombinedOutput()
		return err == nil && re.Match(out)
	})
}

// appReached waits until the app logs, since its latest start, that it has
// its database, and checks it is running. When restarted is true the app was
// (re)started by this step, so it must have started only after the bridge's
// latest run finished.
func (s *stack) appReached(bridge containerState, restarted bool) {
	s.t.Helper()
	s.appLogged("reaching its database", s.bt.appReady)
	app := s.inspect(s.bt.app)
	if !app.State.Running {
		s.t.Errorf("%s is %s, want running", s.bt.app, app.State.Status)
	}
	if restarted && app.State.StartedAt.Before(bridge.State.FinishedAt) {
		s.t.Errorf("%s started at %s, before the bridge finished at %s", s.bt.app, app.State.StartedAt, bridge.State.FinishedAt)
	}
}

// query runs sql as an account with a password, from another container over
// the stack's network, or on the database container's socket for a local
// account. The password travels in the client's environment, never argv. It
// returns the client's standard output, and its error output inside err.
func (s *stack) query(a account, password, sql string) (string, error) {
	img := parseCompose(s.t, readCompose(s.t, s.bt.tile)).Services[s.bt.db].Image
	var args []string
	var pwVar string
	switch {
	case s.bt.tile == "immich":
		pwVar = "PGPASSWORD"
		args = []string{"run", "--rm", "--network", s.project + "_default", "-e", pwVar, "--entrypoint", "psql", img,
			"-w", "-X", "-q", "-tA", "-h", s.bt.db, "-U", a.name, "-d", "immich", "-c", sql}
	case a.local:
		pwVar = "MYSQL_PWD"
		args = []string{"exec", "-e", pwVar, s.containerID(s.bt.db), "mariadb", "-u", a.name, "-N", "-B", "-e", sql}
	default:
		pwVar = "MYSQL_PWD"
		args = []string{"run", "--rm", "--network", s.project + "_default", "-e", pwVar, "--entrypoint", "mariadb", img,
			"--protocol=tcp", "-h", s.bt.db, "-u", a.name, "-N", "-B", "-e", sql}
	}
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), pwVar+"="+password)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, errOut.String())
	}
	return out.String(), nil
}

// authRefusal recognises the database refusing a password, as opposed to any
// other failure, so "refused" is proven and not inferred from an exit code.
var authRefusal = regexp.MustCompile(`ERROR 1045|password authentication failed`)

// logins asserts that every account accepts want and refuses each of refused.
func (s *stack) logins(want map[string]string, refused ...map[string]string) {
	s.t.Helper()
	for _, a := range s.bt.accounts {
		where := a.name
		if a.local {
			where += " (local socket)"
		}
		if _, err := s.query(a, want[a.secret], "SELECT 1"); err != nil {
			s.t.Errorf("%s refused the expected password: %v", where, err)
		}
		for _, r := range refused {
			_, err := s.query(a, r[a.secret], "SELECT 1")
			if err == nil || !authRefusal.MatchString(err.Error()) {
				s.t.Errorf("%s: want a password refusal, got %v", where, err)
			}
		}
	}
}

// writeRow and readRow keep a marker row in the app's own database.
const marker = "written-before-the-upgrade"

func (s *stack) writeRow(password string) {
	s.t.Helper()
	sql := "CREATE TABLE bridge_marker (v VARCHAR(64)); INSERT INTO bridge_marker VALUES ('" + marker + "');"
	if s.bt.tile == "romm" {
		sql = "USE romm; " + sql
	}
	if _, err := s.query(s.bt.accounts[0], password, sql); err != nil {
		s.t.Fatalf("write the marker row: %v", err)
	}
}

func (s *stack) rowSurvived(password string) {
	s.t.Helper()
	sql := "SELECT v FROM bridge_marker;"
	if s.bt.tile == "romm" {
		sql = "SELECT v FROM romm.bridge_marker;"
	}
	out, err := s.query(s.bt.accounts[0], password, sql)
	if err != nil || strings.TrimSpace(out) != marker {
		s.t.Errorf("the pre-upgrade row is gone: err=%v, got %q", err, out)
	}
}

// waitLogin waits until the database accepts the app account's password,
// which is when a legacy stack has finished creating its data directory.
func (s *stack) waitLogin(password string) {
	s.t.Helper()
	s.waitFor(s.bt.db+" accepting a login", func() bool {
		_, err := s.query(s.bt.accounts[0], password, "SELECT 1")
		return err == nil
	})
}

// volumeCopy copies the database volume into (or out of) a side volume, with
// the stack's database stopped.
func (s *stack) volumeCopy(from, to string) {
	s.t.Helper()
	img := parseCompose(s.t, readCompose(s.t, s.bt.tile)).Services[s.bt.db].Image
	out, err := exec.Command("docker", "run", "--rm", "--network", "none",
		"-v", from+":/from:ro", "-v", to+":/to", "--entrypoint", "sh", img,
		"-c", "find /to -mindepth 1 -delete && cp -a /from/. /to/").CombinedOutput()
	if err != nil {
		s.t.Fatalf("copy volume %s to %s: %v: %s", from, to, err, out)
	}
}

func (s *stack) dbVolume() string { return s.project + "_" + s.bt.tile + "-db" }

// assertNoValueLeaked is TC-521-14: no platform value on a bridge's command
// line, or in the bridge's or the database's log.
func (s *stack) assertNoValueLeaked() {
	t := s.t
	for _, svc := range []string{s.bt.bridge, s.bt.db} {
		out, err := exec.Command("docker", "ps", "-a", "-q",
			"--filter", "label=com.docker.compose.project="+s.project,
			"--filter", "label=com.docker.compose.service="+svc).Output()
		if err != nil {
			t.Errorf("list %s: %v", svc, err)
			continue
		}
		for _, id := range strings.Fields(string(out)) {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			ins, _ := exec.Command("docker", "inspect", id).Output()
			var cs []containerState
			_ = json.Unmarshal(ins, &cs)
			argv := ""
			if len(cs) == 1 && svc == s.bt.bridge {
				b, _ := json.Marshal([]any{cs[0].Path, cs[0].Args, cs[0].Config.Cmd, cs[0].Config.Entrypoint})
				argv = string(b)
			}
			for name, v := range s.values {
				if strings.Contains(string(logs), v) {
					t.Errorf("TC-521-14: the %s value appears in the %s log", name, svc)
				}
				if strings.Contains(argv, v) {
					t.Errorf("TC-521-14: the %s value appears on the %s command line", name, svc)
				}
			}
		}
	}
}

// TC-521-06 and TC-521-10: a fresh install.
func testFresh(t *testing.T, bt bridgeTile) {
	s := newStack(t, bt)
	s.upHead()
	s.appReached(s.bridgeRan(0, sayAlready), true)
	s.logins(s.values, bt.legacy)
}

// TC-521-07/08/16/17 for romm, TC-521-11/12/16/17 for immich, in order on
// one set of volumes.
func testUpgradePath(t *testing.T, bt bridgeTile) {
	s := newStack(t, bt)
	legacy := s.legacyCompose(bt.legacy)

	// Before: the published compose creates the database on the literals,
	// the app runs on it, and a row is written.
	s.mustUp(legacy, bt.upTargets...)
	s.waitLogin(bt.legacy[bt.accounts[0].secret])
	s.appLogged("reaching its database on the published compose", bt.appReady)
	s.writeRow(bt.legacy[bt.accounts[0].secret])
	// A backup taken before the upgrade, with the database stopped as the
	// agent's quiesce does.
	if out, err := s.compose(false, "stop"); err != nil {
		t.Fatalf("stop: %v: %s", err, out)
	}
	backup := s.project + "_backup"
	if out, err := exec.Command("docker", "volume", "create", backup).CombinedOutput(); err != nil {
		t.Fatalf("create the backup volume: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", backup).Run() })
	s.volumeCopy(s.dbVolume(), backup)

	t.Run("upgrade", func(t *testing.T) { // TC-521-07, TC-521-11
		s.t = t
		s.upHead()
		s.appReached(s.bridgeRan(0, sayMoved), true)
		s.logins(s.values, bt.legacy)
		s.rowSurvived(s.values[bt.accounts[0].secret])
	})

	t.Run("rerun", func(t *testing.T) { // TC-521-08, TC-521-12
		s.t = t
		before := s.countLines(sayAlready)
		// Nothing changed, so up re-runs the exited bridge and leaves the
		// running app alone.
		s.upHead()
		s.appReached(s.bridgeRan(0, sayAlready), false)
		if out, err := s.compose(false, "stop"); err != nil {
			t.Fatalf("stop: %v: %s", err, out)
		}
		if out, err := s.compose(false, "start"); err != nil {
			t.Fatalf("start: %v: %s", err, out)
		}
		s.appReached(s.bridgeRan(0, sayAlready), true)
		if got := s.countLines(sayAlready); got != before+2 {
			t.Errorf("the bridge said %q %d more times, want 2 (one per run)", sayAlready, got-before)
		}
		s.logins(s.values, bt.legacy)
	})

	t.Run("restore", func(t *testing.T) { // TC-521-16
		s.t = t
		if out, err := s.compose(false, "stop"); err != nil {
			t.Fatalf("stop: %v: %s", err, out)
		}
		s.volumeCopy(backup, s.dbVolume())
		if out, err := s.compose(false, "start"); err != nil {
			t.Fatalf("start after the restore: %v: %s", err, out)
		}
		s.appReached(s.bridgeRan(0, sayMoved), true)
		s.logins(s.values, bt.legacy)
		s.rowSurvived(s.values[bt.accounts[0].secret])
	})

	t.Run("revert", func(t *testing.T) { // TC-521-17
		s.t = t
		s.assertNoValueLeaked() // the revert removes the bridge container
		// The previous compose, re-applied: the app presents the published
		// password to a database that now has the platform's.
		s.mustUp(legacy, bt.upTargets...)
		s.appLogged("being refused by its database", bt.appRefused)
		// The recovery: upgrade again.
		s.upHead()
		s.appReached(s.bridgeRan(0, sayAlready), true)
		s.rowSurvived(s.values[bt.accounts[0].secret])
	})
}

// TC-521-09 and TC-521-13: a database on passwords that are neither the
// platform's nor the published ones.
func testUnknownPasswords(t *testing.T, bt bridgeTile) {
	s := newStack(t, bt)
	other := map[string]string{}
	for name := range bt.legacy {
		other[name] = randomValue(t)
	}
	s.mustUp(s.legacyCompose(other), bt.db)
	s.waitLogin(other[bt.accounts[0].secret])

	out, err := s.up(s.head, bt.upTargets...)
	if err == nil || !strings.Contains(out, bt.bridge) {
		t.Errorf("up: want a failure naming %s, got err=%v:\n%s", bt.bridge, err, out)
	}
	s.bridgeRan(1, sayRefusedBoth)
	for _, l := range strings.Split(s.logs(bt.bridge), "\n") {
		for _, wrong := range []string{sayAlready, sayMoved, sayNoAnswer, "Could not log in", "could not change"} {
			if strings.Contains(l, wrong) {
				t.Errorf("the bridge also logged %q", l)
			}
		}
	}
	if app := s.inspect(bt.app); !app.State.StartedAt.IsZero() {
		t.Errorf("%s started at %s; it must never start", bt.app, app.State.StartedAt)
	}
	s.logins(other)
}

// TC-521-15: a database that never answers. The bridge alone, without its
// database.
func testNoAnswer(t *testing.T, bt bridgeTile) {
	s := newStack(t, bt)
	s.write(s.head)
	if out, err := s.compose(true, "up", "-d", "--no-deps", bt.bridge); err != nil {
		t.Fatalf("up the bridge alone: %v: %s", err, out)
	}
	if out, err := exec.Command("docker", "wait", s.containerID(bt.bridge)).CombinedOutput(); err != nil {
		t.Fatalf("wait for the bridge: %v: %s", err, out)
	}
	st := s.bridgeRan(1, sayNoAnswer)
	took := st.State.FinishedAt.Sub(st.State.StartedAt)
	// The script reads whole seconds, so it may stop up to a second early.
	if took < readinessBound-time.Second || took >= deployBudget {
		t.Errorf("the bridge gave up after %s, want at least its %s bound and under the %s deploy budget", took, readinessBound, deployBudget)
	}
	if strings.Contains(s.logs(bt.bridge), sayRefusedBoth) {
		t.Error("a database that never answered was reported as refusing the passwords")
	}
	t.Logf("gave up after %s", took)
}

func TestRomm(t *testing.T) {
	t.Parallel()
	t.Run("fresh", func(t *testing.T) { testFresh(t, romm) })                        // TC-521-06
	t.Run("upgrade-path", func(t *testing.T) { testUpgradePath(t, romm) })           // TC-521-07, 08, 16, 17
	t.Run("unknown-passwords", func(t *testing.T) { testUnknownPasswords(t, romm) }) // TC-521-09
}

func TestImmich(t *testing.T) {
	t.Parallel()
	t.Run("fresh", func(t *testing.T) { testFresh(t, immich) })                        // TC-521-10
	t.Run("upgrade-path", func(t *testing.T) { testUpgradePath(t, immich) })           // TC-521-11, 12, 16, 17
	t.Run("unknown-passwords", func(t *testing.T) { testUnknownPasswords(t, immich) }) // TC-521-13
}

// TC-521-15, both tiles at once: each waits out the full bound.
func TestNoAnswer(t *testing.T) {
	t.Parallel()
	for _, bt := range []bridgeTile{romm, immich} {
		t.Run(bt.tile, func(t *testing.T) {
			t.Parallel()
			testNoAnswer(t, bt)
		})
	}
}
