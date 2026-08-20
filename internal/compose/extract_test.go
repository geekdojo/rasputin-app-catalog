package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

const pinned = "nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestExtract_NamedVolumesAreNotBindMounts(t *testing.T) {
	// The distinction the whole bind-mount rule rests on: in the short form,
	// "name:/target" is a named volume and "/path:/target" is a bind.
	f, err := Extract([]byte(`
services:
  app:
    image: ` + pinned + `
    volumes:
      - app-config:/config
      - /var/lib/rasputin/apps/app/data:/data
      - ./relative:/rel
`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(f.BindMounts) != 2 {
		t.Fatalf("bind mounts = %v, want the two paths and not the named volume", f.BindMounts)
	}
	for _, b := range f.BindMounts {
		if b == "app-config" {
			t.Errorf("named volume %q was misread as a bind mount", b)
		}
	}
}

func TestExtract_LongFormBindMount(t *testing.T) {
	f, err := Extract([]byte(`
services:
  app:
    image: ` + pinned + `
    volumes:
      - type: bind
        source: /etc/shadow
        target: /x
      - type: volume
        source: named
        target: /y
`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(f.BindMounts) != 1 || f.BindMounts[0] != "/etc/shadow" {
		t.Fatalf("bind mounts = %v, want only the long-form bind", f.BindMounts)
	}
}

func TestExtract_PrivilegeFlags(t *testing.T) {
	cases := []struct {
		name  string
		yml   string
		check func(t *testing.T, f tileschema.SafetyFacts)
	}{
		{"privileged", "    privileged: true\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if !f.Privileged {
				t.Error("privileged not detected")
			}
		}},
		{"host network", "    network_mode: host\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if !f.HostNetwork {
				t.Error("host networking not detected")
			}
		}},
		{"host pid", "    pid: host\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if !f.HostPIDOrIPC {
				t.Error("host pid not detected")
			}
		}},
		{"host ipc", "    ipc: host\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if !f.HostPIDOrIPC {
				t.Error("host ipc not detected")
			}
		}},
		{"cap_add", "    cap_add:\n      - NET_ADMIN\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if len(f.CapAdd) != 1 || f.CapAdd[0] != "NET_ADMIN" {
				t.Errorf("cap_add = %v", f.CapAdd)
			}
		}},
		{"devices", "    devices:\n      - /dev/bus/usb:/dev/bus/usb\n", func(t *testing.T, f tileschema.SafetyFacts) {
			if len(f.Devices) != 1 || f.Devices[0] != "/dev/bus/usb" {
				t.Errorf("devices = %v, want the host side only", f.Devices)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Extract([]byte("services:\n  app:\n    image: " + pinned + "\n" + tc.yml))
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			tc.check(t, got)
		})
	}
}

// Case-insensitivity matters: compose accepts "Host" and a check that only
// matched lowercase would wave through the exact thing it exists to catch.
func TestExtract_HostModeIsCaseInsensitive(t *testing.T) {
	f, err := Extract([]byte("services:\n  app:\n    image: " + pinned + "\n    network_mode: HOST\n"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !f.HostNetwork {
		t.Error("network_mode: HOST was not detected")
	}
}

func TestExtract_MultipleServicesAggregate(t *testing.T) {
	f, err := Extract([]byte(`
services:
  a:
    image: ` + pinned + `
  b:
    image: redis@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    privileged: true
`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(f.Images) != 2 {
		t.Errorf("images = %v, want both services", f.Images)
	}
	if !f.Privileged {
		t.Error("privileged on any service must set the flag for the stack")
	}
}

func TestExtract_Rejects(t *testing.T) {
	cases := []struct{ name, yml, want string }{
		{"no services", "version: '3'\n", "no services"},
		{"service without image", "services:\n  a:\n    restart: always\n", "no image"},
		{"build from source", "services:\n  a:\n    build: .\n", "builds from source"},
		{"malformed", "services:\n  a:\n   - [\n", "parse compose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Extract([]byte(tc.yml))
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// --- #195: keys the extractor used to be blind to. ---

// The regression that started #195. This exact compose — no privileged, no
// cap_add, no bind mounts, no docker socket — lets buildah build container
// images, verified by running it. Before this change it produced SafetyFacts
// indistinguishable from an unprivileged tile's.
func TestExtract_SeccompUnconfinedIsSeen(t *testing.T) {
	f, err := Extract([]byte(`
services:
  runner:
    image: ` + pinned + `
    security_opt:
      - seccomp=unconfined
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(f.SecurityOpt) != 1 || f.SecurityOpt[0] != "seccomp=unconfined" {
		t.Fatalf("security_opt not captured: %#v", f.SecurityOpt)
	}
	// It must not be laundered into an existing flag — the whole point is that
	// it is a distinct fact the policy can rule on separately.
	if f.Privileged || len(f.CapAdd) != 0 {
		t.Errorf("seccomp=unconfined must not be reported as privileged or cap_add: %#v", f)
	}
}

// Compose accepts "key=value" and "key:value" for security_opt, and a value may
// itself contain a colon. Splitting on the wrong separator turns label=user:USER
// into a fact nobody can match on.
func TestExtract_SecurityOptSeparatorsNormalize(t *testing.T) {
	f, err := Extract([]byte(`
services:
  app:
    image: ` + pinned + `
    security_opt:
      - apparmor:unconfined
      - label=user:USER
      - no-new-privileges:true
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := []string{"apparmor=unconfined", "label=user:USER", "no-new-privileges=true"}
	if strings.Join(f.SecurityOpt, ",") != strings.Join(want, ",") {
		t.Errorf("security_opt = %v, want %v", f.SecurityOpt, want)
	}
}

// GPUs are requested through deploy.resources.reservations.devices, not the
// top-level devices: key — so before this change a stack could reach hardware
// while declaring no devices and therefore escaping the needsHardware check.
func TestExtract_DeployReservedDevicesAreSeen(t *testing.T) {
	f, err := Extract([]byte(`
services:
  ml:
    image: ` + pinned + `
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu, compute]
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(f.ReservedDevices) != 1 || f.ReservedDevices[0] != "driver=nvidia,capabilities=compute+gpu" {
		t.Fatalf("reserved devices = %#v", f.ReservedDevices)
	}
	if len(f.Devices) != 0 {
		t.Errorf("a deploy reservation is not a devices: entry; got Devices=%v", f.Devices)
	}
}

// sysctls has two spellings and reading only one is the same class of bug.
func TestExtract_SysctlsBothSpellings(t *testing.T) {
	for name, yml := range map[string]string{
		"mapping": "    sysctls:\n      net.ipv4.ip_forward: 1\n",
		"list":    "    sysctls:\n      - net.ipv4.ip_forward=1\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Extract([]byte("services:\n  app:\n    image: " + pinned + "\n" + yml))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if len(f.Sysctls) != 1 || f.Sysctls[0] != "net.ipv4.ip_forward=1" {
				t.Fatalf("sysctls = %#v", f.Sysctls)
			}
		})
	}
}

// A `service:` target names a sibling the signed bundle describes — the
// ordinary VPN-sidecar shape. A `container:` target names something outside it.
// Both are joins; only one is covered by the bundle, so they must be
// distinguishable rather than collapsed.
func TestExtract_NamespaceJoinsDistinguishServiceFromContainer(t *testing.T) {
	f, err := Extract([]byte(`
services:
  vpn:
    image: ` + pinned + `
  app:
    image: ` + pinned + `
    network_mode: "service:vpn"
    pid: "container:something-else"
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := []string{"network:service:vpn", "pid:container:something-else"}
	if strings.Join(f.NamespaceJoins, ",") != strings.Join(want, ",") {
		t.Errorf("namespace joins = %v, want %v", f.NamespaceJoins, want)
	}
	// host stays where it was; duplicating a fact under two names invites a
	// validator that checks one and misses the other.
	if f.HostNetwork || f.HostPIDOrIPC {
		t.Errorf("service:/container: joins are not host mode: %#v", f)
	}
}

func TestExtract_HostModeIsNotAlsoANamespaceJoin(t *testing.T) {
	f, err := Extract([]byte(`
services:
  app:
    image: ` + pinned + `
    network_mode: host
    pid: host
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !f.HostNetwork || !f.HostPIDOrIPC {
		t.Fatalf("host mode lost: %#v", f)
	}
	if len(f.NamespaceJoins) != 0 {
		t.Errorf("host must not be double-reported as a join: %v", f.NamespaceJoins)
	}
}

// The corpus check the issue asks for: one compose using every newly-read key,
// asserting each lands somewhere. A key added to the struct but never populated
// looks exactly like a key that is genuinely absent.
func TestExtract_EveryPrivilegeKeyRoundTrips(t *testing.T) {
	f, err := Extract([]byte(`
services:
  app:
    image: ` + pinned + `
    security_opt: ["seccomp=unconfined"]
    userns_mode: host
    group_add: [docker, 998]
    sysctls: ["kernel.shmmax=1"]
    volumes_from: ["other:ro"]
    cgroup_parent: /rasputin
    tmpfs: /tmp/cache
    ulimits:
      nofile: 65535
    deploy:
      resources:
        reservations:
          devices:
            - capabilities: [gpu]
`))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, c := range []struct {
		name string
		got  []string
	}{
		{"SecurityOpt", f.SecurityOpt}, {"UsernsMode", f.UsernsMode},
		{"GroupAdd", f.GroupAdd}, {"Sysctls", f.Sysctls},
		{"VolumesFrom", f.VolumesFrom}, {"CgroupParent", f.CgroupParent},
		{"Tmpfs", f.Tmpfs}, {"Ulimits", f.Ulimits},
		{"ReservedDevices", f.ReservedDevices},
	} {
		if len(c.got) == 0 {
			t.Errorf("%s was not captured", c.name)
		}
	}
	// group_add accepts a numeric GID; dropping it would leave a host-group
	// membership invisible.
	if strings.Join(f.GroupAdd, ",") != "998,docker" {
		t.Errorf("GroupAdd = %v, want [998 docker]", f.GroupAdd)
	}
}

// The facts are covered by the bundle signature, so identical input must give
// byte-identical output. A set iterated in Go's random map order would make a
// publish unreproducible, and it would fail intermittently rather than always.
func TestExtract_IsDeterministic(t *testing.T) {
	yml := []byte(`
services:
  a:
    image: ` + pinned + `
    security_opt: ["seccomp=unconfined", "apparmor=unconfined", "no-new-privileges=true"]
    group_add: [docker, disk, video, audio]
    sysctls: ["a=1", "b=2", "c=3"]
  b:
    image: ` + pinned + `
    security_opt: ["label=disable"]
    group_add: [render]
`)
	first, err := Extract(yml)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := Extract(yml)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("run %d differs from the first — extraction is not deterministic\nfirst: %#v\ngot:   %#v", i, first, got)
		}
	}
}

// An ordinary tile must not grow ten empty arrays in its signed manifest.
func TestExtract_UnprivilegedTileStaysQuiet(t *testing.T) {
	f, err := Extract([]byte("services:\n  app:\n    image: " + pinned + "\n"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	empty := tileschema.SafetyFacts{Images: f.Images}
	if !reflect.DeepEqual(f, empty) {
		t.Errorf("an unprivileged stack produced non-empty privilege facts: %#v", f)
	}
}
