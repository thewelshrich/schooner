package semver

import "testing"

func TestValidAndCompare(t *testing.T) {
	for _, value := range []string{"v0.0.0", "v1.2.3", "v1.2.3-alpha.1", "v999999999999999999999.0.1+build.7"} {
		if !Valid(value) {
			t.Errorf("Valid(%q) = false", value)
		}
	}
	for _, value := range []string{"1.2.3", "v1.2", "v01.2.3", "v1.2.3-01", "v1.2.3+"} {
		if Valid(value) {
			t.Errorf("Valid(%q) = true", value)
		}
	}
	if !Stable("v1.2.3") || !Stable("v1.2.3+build.7") || Stable("v1.2.3-rc.1") || Stable("dev") {
		t.Error("Stable did not distinguish stable releases from prereleases and development builds")
	}
	tests := []struct {
		left, right string
		want        int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v2.0.0", "v10.0.0", -1},
		{"v1.0.0-alpha", "v1.0.0", -1},
		{"v1.0.0-alpha.2", "v1.0.0-alpha.10", -1},
		{"v1.0.0+one", "v1.0.0+two", 0},
	}
	for _, test := range tests {
		if got, ok := Compare(test.left, test.right); !ok || got != test.want {
			t.Errorf("Compare(%q, %q) = %d, %t; want %d, true", test.left, test.right, got, ok, test.want)
		}
	}
}
