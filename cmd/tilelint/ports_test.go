package main

import (
	"strings"
	"testing"
)

// TC-551-14: tilelint reports the same web-port refusals BuildBundle does,
// verbatim: the port, the failure named, and for the off-loopback case what
// to write instead.
func TestLint_WebPortOffLoopbackOrUnpublished_TC_551_14(t *testing.T) {
	const image = "    image: ghcr.io/example/app@" + testDigest + "\n"
	cases := []struct {
		id, ports string
		want      []string
	}{
		{"all-interfaces", "    ports:\n      - \"8080:80\"\n",
			[]string{"web port 8080", "every interface", "127.0.0.1:"}},
		{"unpublished", "",
			[]string{"web port 8080 is not published"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			root := t.TempDir()
			writeTile(t, root, tc.id, baseTile(tc.id), "services:\n  app:\n"+image+tc.ports)
			problems, _, _ := checkTile(root, tc.id, false)
			if len(problems) != 1 {
				t.Fatalf("want exactly the web-port refusal, got %q", problems)
			}
			for _, want := range tc.want {
				if !strings.Contains(problems[0], want) {
					t.Errorf("problem %q does not contain %q", problems[0], want)
				}
			}
		})
	}
}
