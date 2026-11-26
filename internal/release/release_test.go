package release

import (
	"reflect"
	"testing"
)

func TestPathTakesSupportedHops(t *testing.T) {
	c := Default()
	got, err := c.Path("4.2.0", "5.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"4.3.0", "5.0.0", "5.1.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("path = %v, want %v", got, want)
	}
	if p, _ := c.Path("5.0.0", "5.0.0"); len(p) != 0 {
		t.Fatalf("same version should need no steps, got %v", p)
	}
}

func TestPathRefusesDowngradesAndUnknownVersions(t *testing.T) {
	c := Default()
	for _, tc := range [][2]string{{"5.0.0", "4.3.0"}, {"4.2.0", "9.9.9"}, {"0.1.0", "5.0.0"}} {
		if _, err := c.Path(tc[0], tc[1]); err == nil {
			t.Errorf("%s → %s should fail", tc[0], tc[1])
		}
	}
}

func TestParseRejectsBrokenCatalogs(t *testing.T) {
	bad := []string{
		`{"releases":[{"version":"1.0.0","upgradeFrom":["0.9.0"]}]}`,
		`{"releases":[{"version":"1.0.0"},{"version":"1.0.0"}]}`,
		`{"releases":[{"version":"1.0.0","upgradeFrom":["2.0.0"]},{"version":"2.0.0"}]}`,
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("expected error for %s", b)
		}
	}
}

func TestCompare(t *testing.T) {
	if Compare("4.10.0", "4.9.0") != 1 || Compare("5.0", "5.0.0") != 0 || Compare("4.2.0", "4.3.0") != -1 {
		t.Fatal("version ordering is wrong")
	}
}
