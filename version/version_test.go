package version

import "testing"

func TestIsKnown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		current string
		want    bool
	}{
		{"dev", false},
		{"", false},
		{"1.2.3", false}, // semver requires the "v" prefix
		{"v1.2.3", true},
		{"v1.2.3-rc.1", true},
	}
	for _, tc := range cases {
		t.Run(tc.current, func(t *testing.T) {
			old := Current
			Current = tc.current
			defer func() { Current = old }()
			if got := IsKnown(); got != tc.want {
				t.Fatalf("IsKnown(%q) = %v, want %v", tc.current, got, tc.want)
			}
		})
	}
}

func TestHasUpdate(t *testing.T) {
	t.Parallel()

	old := Current
	defer func() { Current = old }()
	Current = "v1.2.3"

	cases := []struct {
		latest string
		want   bool
	}{
		{"v1.2.4", true},
		{"v2.0.0", true},
		{"v1.2.3", false},
		{"v1.2.2", false},
		{"v1.2.3-rc.1", false}, // prerelease of the current version
		{"v1.2.4-rc.1", true},
		{"dev", false}, // unparsable latest must never claim an update
		{"", false},
		{"1.2.4", false}, // missing "v" prefix
	}
	for _, tc := range cases {
		t.Run(tc.latest, func(t *testing.T) {
			if got := HasUpdate(tc.latest); got != tc.want {
				t.Fatalf("HasUpdate(%q) = %v, want %v", tc.latest, got, tc.want)
			}
		})
	}

	// Unknown current version ("dev" builds) never reports an update.
	Current = "dev"
	for _, latest := range []string{"v99.0.0", ""} {
		if HasUpdate(latest) {
			t.Fatalf("HasUpdate(%q) with dev current should be false", latest)
		}
	}
}
