//go:build functional

package legacybridge

import (
	"encoding/json"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// vaultwarden has no bridge; the stack helpers need only its tile and app.
var vaultwarden = bridgeTile{tile: "vaultwarden", app: "vaultwarden"}

// Synthetic client-side values. A Bitwarden client derives the password hash
// and encrypts the keys and every cipher field before they leave it; the
// server stores them as opaque strings, so any well-formed text stands in.
const (
	vwHash       = "c3ludGhldGljLW1hc3Rlci1wYXNzd29yZC1oYXNo"
	vwKey        = "2.c3ludGhldGljLWl2|c3ludGhldGljLWtleQ==|c3ludGhldGljLW1hYw=="
	vwCipherName = "2.dGMtNTUyLWl2|dGMtNTUyLWl0ZW0tbmFtZQ==|dGMtNTUyLW1hYw=="
	vwDeviceID   = "5f6a0552-0000-4000-8000-000000000552"
)

// vwResp is one HTTP answer from the vaultwarden container.
type vwResp struct {
	code int
	body string
}

// vwShell runs a shell script inside the vaultwarden container. The script
// reads the admin token from the container's own environment, so the token
// stays off the host's command line; curl writes the status on the last line.
func (s *stack) vwShell(script string, args ...string) vwResp {
	s.t.Helper()
	cmd := append([]string{"exec", s.containerID("vaultwarden"), "sh", "-c", script, "sh"}, args...)
	out, err := exec.Command("docker", cmd...).CombinedOutput()
	if err != nil {
		s.t.Fatalf("curl in vaultwarden: %v: %s", err, out)
	}
	text := strings.TrimRight(string(out), "\n")
	i := strings.LastIndex(text, "\n")
	code, err := strconv.Atoi(text[i+1:])
	if err != nil {
		s.t.Fatalf("no HTTP status in curl output: %q", text)
	}
	return vwResp{code: code, body: text[:max(i, 0)]}
}

// vwCall runs curl against vaultwarden's own web server with args.
func (s *stack) vwCall(args ...string) vwResp {
	s.t.Helper()
	return s.vwShell(`curl -sS -w '\n%{http_code}' "$@"`, args...)
}

// vwJSON POSTs a JSON body to path, with an optional bearer token.
func (s *stack) vwJSON(path, body, bearer string) vwResp {
	s.t.Helper()
	args := []string{"-X", "POST", "-H", "Content-Type: application/json", "-H", "Accept: application/json",
		"--data-binary", body}
	if bearer != "" {
		args = append(args, "-H", "Authorization: Bearer "+bearer)
	}
	return s.vwCall(append(args, "http://127.0.0.1:80"+path)...)
}

// vwAlive waits until vaultwarden answers its liveness endpoint.
func (s *stack) vwAlive() {
	s.t.Helper()
	s.waitFor("vaultwarden answering /alive", func() bool {
		return exec.Command("docker", "exec", s.containerID("vaultwarden"),
			"curl", "-fsS", "-o", "/dev/null", "http://127.0.0.1:80/alive").Run() == nil
	})
}

// vwRegistrationRefused asserts an uninvited email cannot start registration.
func (s *stack) vwRegistrationRefused(email string) {
	s.t.Helper()
	r := s.vwJSON("/identity/accounts/register/send-verification-email", `{"email":"`+email+`","name":"x"}`, "")
	if r.code < 400 || !strings.Contains(r.body, "Registration not allowed") {
		s.t.Errorf("uninvited registration for %s: got %d %s, want a refusal saying \"Registration not allowed\"", email, r.code, r.body)
	}
}

// vwRegister registers email through the verification-token flow the web
// vault uses, failing the test unless each step succeeds.
func (s *stack) vwRegister(email string) {
	s.t.Helper()
	r := s.vwJSON("/identity/accounts/register/send-verification-email", `{"email":"`+email+`","name":"Owner"}`, "")
	var token string
	if r.code != 200 || json.Unmarshal([]byte(r.body), &token) != nil || token == "" {
		s.t.Fatalf("start registration for %s: got %d %s, want a verification token", email, r.code, r.body)
	}
	body, _ := json.Marshal(map[string]any{
		"email": email, "emailVerificationToken": token,
		"masterPasswordHash": vwHash, "userSymmetricKey": vwKey,
		"kdf": 0, "kdfIterations": 600000,
		"userAsymmetricKeys": map[string]string{"publicKey": "c3ludGhldGljLXB1YmxpYw==", "encryptedPrivateKey": vwKey},
	})
	if r := s.vwJSON("/identity/accounts/register/finish", string(body), ""); r.code != 200 {
		s.t.Fatalf("finish registration for %s: got %d %s", email, r.code, r.body)
	}
}

// vwLogin logs email in with the password grant the clients use and returns
// the access token.
func (s *stack) vwLogin(email string) string {
	s.t.Helper()
	form := url.Values{
		"grant_type": {"password"}, "username": {email}, "password": {vwHash},
		"scope": {"api offline_access"}, "client_id": {"web"},
		"deviceIdentifier": {vwDeviceID}, "deviceName": {"tc-552"}, "deviceType": {"9"},
	}
	r := s.vwCall("-X", "POST", "-H", "Content-Type: application/x-www-form-urlencoded",
		"--data-binary", form.Encode(), "http://127.0.0.1:80/identity/connect/token")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if r.code != 200 || json.Unmarshal([]byte(r.body), &tok) != nil || tok.AccessToken == "" {
		s.t.Fatalf("log in as %s: got %d %s", email, r.code, r.body)
	}
	return tok.AccessToken
}

// vwUserID is the account's id, which a client names as the key owner of
// every cipher it encrypts.
func (s *stack) vwUserID(bearer string) string {
	s.t.Helper()
	r := s.vwCall("-H", "Authorization: Bearer "+bearer, "http://127.0.0.1:80/api/accounts/profile")
	var p struct {
		ID string `json:"id"`
	}
	if r.code != 200 || json.Unmarshal([]byte(r.body), &p) != nil || p.ID == "" {
		s.t.Fatalf("read the account profile: got %d %s", r.code, r.body)
	}
	return p.ID
}

// vwCipherNames lists the names of the ciphers the account holds.
func (s *stack) vwCipherNames(bearer string) []string {
	s.t.Helper()
	r := s.vwCall("-H", "Authorization: Bearer "+bearer, "http://127.0.0.1:80/api/ciphers")
	var list struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if r.code != 200 || json.Unmarshal([]byte(r.body), &list) != nil {
		s.t.Fatalf("list ciphers: got %d %s", r.code, r.body)
	}
	var names []string
	for _, c := range list.Data {
		names = append(names, c.Name)
	}
	return names
}

// vwAdminLogin signs in to /admin, with the container's own ADMIN_TOKEN when
// token is empty, keeping the session cookie in the container for vwInvite.
func (s *stack) vwAdminLogin(token string) vwResp {
	s.t.Helper()
	return s.vwShell(`t="${1:-$ADMIN_TOKEN}"
curl -sS -w '\n%{http_code}' -c /tmp/tc552-admin --data-urlencode "token=$t" http://127.0.0.1:80/admin`, token)
}

// vwAdminAccepts asserts /admin refuses a wrong token and admits the
// container's own.
func (s *stack) vwAdminAccepts() {
	s.t.Helper()
	if r := s.vwAdminLogin("not-the-admin-token"); r.code != 401 || !strings.Contains(r.body, "Invalid admin token") {
		s.t.Errorf("/admin with a wrong token: got %d, want 401 \"Invalid admin token\"", r.code)
	}
	if r := s.vwAdminLogin(""); r.code != 200 {
		s.t.Fatalf("/admin with the token: got %d %s, want 200", r.code, r.body)
	}
}

// vwInvite invites email from the admin page, using vwAdminLogin's session.
func (s *stack) vwInvite(email string) {
	s.t.Helper()
	r := s.vwCall("-b", "/tmp/tc552-admin", "-X", "POST", "-H", "Content-Type: application/json",
		"--data-binary", `{"email":"`+email+`"}`, "http://127.0.0.1:80/admin/invite")
	if r.code != 200 {
		s.t.Fatalf("invite %s from /admin: got %d %s", email, r.code, r.body)
	}
}

// vaultwardenV32 is the compose catalog v32 shipped, rebuilt from the head:
// the same image and volume, with sign-ups open and no admin token. Those two
// environment entries are the whole of the v32 to v33 change.
func vaultwardenV32(t *testing.T, head string) string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(head), &doc); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	env := doc["services"].(map[string]any)["vaultwarden"].(map[string]any)["environment"].(map[string]any)
	env["SIGNUPS_ALLOWED"] = "true"
	delete(env, "ADMIN_TOKEN")
	return marshal(t, doc)
}

// TC-552-13: a fresh install with the shipped compose. With the optional
// admin token unset (the empty value the control plane sends), the admin page
// is disabled and nobody can register: the vault fails closed. Once the owner
// sets the token and the app is redeployed, /admin admits only that token, the
// owner invites their own email, registers it and logs in, while an uninvited
// email is still refused.
func TestVaultwardenFreshInstall_TC_552_13(t *testing.T) {
	t.Parallel()
	s := newStack(t, vaultwarden)
	token := s.values["admin-token"]
	if token == "" {
		t.Fatal("the shipped compose does not use the admin-token secret")
	}

	s.values["admin-token"] = ""
	s.upHead()
	s.vwAlive()
	if r := s.vwCall("http://127.0.0.1:80/admin"); !strings.Contains(r.body, "admin panel is disabled") {
		t.Errorf("/admin with no token: got %d %s, want the \"admin panel is disabled\" page", r.code, r.body)
	}
	s.vwRegistrationRefused("stranger@example.test")

	s.values["admin-token"] = token
	s.upHead()
	s.vwAlive()
	s.vwRegistrationRefused("stranger@example.test")
	s.vwAdminAccepts()
	s.vwInvite("owner@example.test")
	s.vwRegister("owner@example.test")
	s.vwLogin("owner@example.test")
	s.vwRegistrationRefused("stranger@example.test")
}

// TC-552-14: an install on the catalog v32 compose, with an account and a
// stored item, brought up on the shipped compose in the same project and
// volume: the account logs in and its item is returned, a new uninvited
// registration is refused, and /admin accepts the owner's token.
func TestVaultwardenUpgradeFromV32_TC_552_14(t *testing.T) {
	t.Parallel()
	s := newStack(t, vaultwarden)
	const owner = "existing@example.test"

	s.mustUp(vaultwardenV32(t, s.head))
	s.vwAlive()
	s.vwRegister(owner)
	bearer := s.vwLogin(owner)
	if r := s.vwJSON("/api/ciphers", `{"type":2,"name":"`+vwCipherName+`","encryptedFor":"`+s.vwUserID(bearer)+
		`","notes":null,"favorite":false,"secureNote":{"type":0}}`, bearer); r.code != 200 {
		t.Fatalf("store an item: got %d %s", r.code, r.body)
	}
	before := s.containerID("vaultwarden")

	s.upHead()
	if s.containerID("vaultwarden") == before {
		t.Fatal("the upgrade did not recreate the vaultwarden container")
	}
	s.vwAlive()
	names := s.vwCipherNames(s.vwLogin(owner))
	if len(names) != 1 || names[0] != vwCipherName {
		t.Errorf("after the upgrade the account holds %q, want the one item stored before it", names)
	}
	s.vwRegistrationRefused("stranger@example.test")
	s.vwAdminAccepts()
}
