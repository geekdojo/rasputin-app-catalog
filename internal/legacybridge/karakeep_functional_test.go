//go:build functional

package legacybridge

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// karakeep has no bridge; the stack helpers need only its tile and app.
var karakeep = bridgeTile{tile: "karakeep", app: "karakeep"}

// The directories karakeep's processes run in: the image's working directory
// and the cd of each s6 service (init-db-migration, svc-web, svc-workers).
// With DATA_DIR unset each process opens ./db.db in its own (#885).
var karakeepWorkDirs = []string{"/app", "/db_migrations", "/app/apps/web", "/app/apps/workers"}

// karakeepCall runs curl inside the karakeep container against its own web
// server, so the test publishes no port. It returns the response body.
func (s *stack) karakeepCall(path, body string) string {
	s.t.Helper()
	out, err := exec.Command("docker", "exec", s.containerID("karakeep"), "curl", "-sS", "-X", "POST",
		"-H", "Content-Type: application/json", "-H", "Origin: http://localhost:3000",
		"--data-binary", body, "http://127.0.0.1:3000"+path).CombinedOutput()
	if err != nil {
		s.t.Fatalf("POST %s: %v: %s", path, err, out)
	}
	return string(out)
}

// karakeepUser returns the user in a sign-up or sign-in response, failing
// when there is none.
func (s *stack) karakeepUser(what, resp string) (id, email string) {
	s.t.Helper()
	var r struct {
		User struct{ ID, Email string } `json:"user"`
	}
	if err := json.Unmarshal([]byte(resp), &r); err != nil || r.User.ID == "" {
		s.t.Fatalf("%s: want a user, got %s", what, resp)
	}
	return r.User.ID, r.User.Email
}

// karakeepHealthy waits until the web server answers its health check, which
// it does only after the database migrations have run.
func (s *stack) karakeepHealthy() {
	s.t.Helper()
	s.waitFor("karakeep answering /api/health", func() bool {
		return exec.Command("docker", "exec", s.containerID("karakeep"),
			"wget", "-q", "-O-", "http://127.0.0.1:3000/api/health").Run() == nil
	})
}

// karakeepDB asserts /data/db.db exists in the stack's karakeep-data volume
// and that no working directory holds a db.db. It returns the file's inode.
func (s *stack) karakeepDB() string {
	s.t.Helper()
	id := s.containerID("karakeep")
	out, err := exec.Command("docker", "inspect", "--format",
		`{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, id).Output()
	if want := s.project + "_karakeep-data"; err != nil || strings.TrimSpace(string(out)) != want {
		s.t.Errorf("/data is mounted from %q (err %v), want the volume %s", strings.TrimSpace(string(out)), err, want)
	}
	ino, err := exec.Command("docker", "exec", id, "stat", "-c", "%i", "/data/db.db").CombinedOutput()
	if err != nil {
		s.t.Fatalf("/data/db.db is not in the volume: %v: %s", err, ino)
	}
	for _, dir := range karakeepWorkDirs {
		if exec.Command("docker", "exec", id, "test", "-e", dir+"/db.db").Run() == nil {
			s.t.Errorf("%s/db.db exists: the database is outside the volume", dir)
		}
	}
	return strings.TrimSpace(string(ino))
}

// TC-521-21: karakeep keeps its database inside its data volume, and a user
// survives a force-recreate of the karakeep container (#885).
func TestKarakeepDatabaseSurvivesRecreate(t *testing.T) {
	t.Parallel()
	s := newStack(t, karakeep)
	s.upHead()
	s.karakeepHealthy()

	const email, password = "tc-521-21@example.com", "tc-521-21-password"
	creds := `{"email":"` + email + `","password":"` + password + `"}`
	userID, _ := s.karakeepUser("sign-up", s.karakeepCall("/api/auth/sign-up/email",
		`{"name":"TC-521-21","email":"`+email+`","password":"`+password+`"}`))
	inode := s.karakeepDB()
	before := s.containerID("karakeep")

	if out, err := s.compose(true, "up", "-d", "--force-recreate", "karakeep"); err != nil {
		t.Fatalf("force-recreate karakeep: %v: %s", err, out)
	}
	if after := s.containerID("karakeep"); after == before {
		t.Fatalf("the karakeep container %s was not recreated", before)
	}
	s.karakeepHealthy()

	if got := s.karakeepDB(); got != inode {
		t.Errorf("/data/db.db is inode %s after the recreate, want the same file (inode %s)", got, inode)
	}
	id, gotEmail := s.karakeepUser("sign-in after the recreate", s.karakeepCall("/api/auth/sign-in/email", creds))
	if id != userID || gotEmail != email {
		t.Errorf("signed in as %s (%s), want the user created before the recreate, %s (%s)", id, gotEmail, userID, email)
	}
}
