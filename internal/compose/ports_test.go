package compose

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/geekdojo/rasputin-control-plane/tileschema"
)

// TC-551-10: the two short forms the catalog uses are read into the host side
// of the publish.
func TestPortBindings_SupportedForms(t *testing.T) {
	cases := []struct {
		entry string
		want  []tileschema.PortBinding
	}{
		{"8096:8096", []tileschema.PortBinding{{HostIP: "", Published: 8096}}},
		{"127.0.0.1:8085:8080", []tileschema.PortBinding{{HostIP: "127.0.0.1", Published: 8085}}},
		// The ends of the port range.
		{"1:65535", []tileschema.PortBinding{{HostIP: "", Published: 1}}},
		{"65535:1", []tileschema.PortBinding{{HostIP: "", Published: 65535}}},
	}
	for _, tc := range cases {
		got, err := portBindings("web", []any{tc.entry})
		if err != nil {
			t.Fatalf("%q: %v", tc.entry, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %+v, want %+v", tc.entry, got, tc.want)
		}
	}
}

// TC-551-11: every other form fails closed, naming the service and quoting the
// entry, rather than being skipped as a binding nobody judged (F-551-02).
func TestPortBindings_UnsupportedFormsRefused(t *testing.T) {
	cases := map[string]any{
		"range":          "8000-8010:8000-8010",
		"long syntax":    map[string]any{"target": 80, "published": 8080},
		"variable":       "${PORT}:80",
		"bracketed IPv6": "[::1]:8096:8096",
		"protocol":       "8096:8096/udp",
		"bare int":       8096,
		"bare string":    "8096",
		"non-IPv4 host":  "localhost:8096:8096",
		"out of range":   "70000:80",
		"port zero":      "0:80",
		"one past max":   "80:65536",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := portBindings("web", []any{entry})
			if err == nil {
				t.Fatalf("%v must be refused", entry)
			}
			for _, want := range []string{`service "web"`, fmt.Sprintf("%q", fmt.Sprint(entry))} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %s", err, want)
				}
			}
		})
	}
}
