package compose

import (
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
