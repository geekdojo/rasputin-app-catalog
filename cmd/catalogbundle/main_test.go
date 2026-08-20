package main

import "testing"

// SOURCE_DATE_EPOCH is what makes a rebuild of the same commit byte-identical.
// Without it the bundle differs in exactly one field, which is enough to make
// "is this the artifact we reviewed?" unanswerable by comparing hashes.
func TestPublishTime(t *testing.T) {
	cases := []struct {
		name, flag, epoch, want string
		wantErr                 bool
	}{
		{name: "flag wins over epoch", flag: "2026-01-02T03:04:05Z", epoch: "0", want: "2026-01-02T03:04:05Z"},
		{name: "flag is normalised to UTC", flag: "2026-01-02T03:04:05+02:00", want: "2026-01-02T01:04:05Z"},
		{name: "epoch is honoured", epoch: "1750000000", want: "2025-06-15T15:06:40Z"},
		{name: "bad flag is fatal", flag: "yesterday", wantErr: true},
		{name: "bad epoch is fatal", epoch: "not-a-number", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := publishTime(c.flag, c.epoch)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("publishTime: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// With neither supplied it must still produce a parseable RFC 3339 stamp
// rather than an empty string, since the field is displayed to an operator.
func TestPublishTime_DefaultsToNow(t *testing.T) {
	got, err := publishTime("", "")
	if err != nil {
		t.Fatalf("publishTime: %v", err)
	}
	if got == "" {
		t.Fatal("publish time must never be empty")
	}
	if _, err := publishTime(got, ""); err != nil {
		t.Errorf("the default must itself be valid RFC 3339, got %q", got)
	}
}
