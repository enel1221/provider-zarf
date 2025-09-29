package zarfclient

import (
	"context"
	"testing"
)

// TestInferClusterName ensures heuristic works.
func TestInferClusterName(t *testing.T) {
	cases := []struct {
		in string
		is bool
	}{
		{"mypkg", true},
		{"ghcr.io/org/pkg:0.1.0", false},
		{"registry.local/pkg@sha256:deadbeef", false},
	}
	for _, c := range cases {
		_, got := inferClusterPackageName(c.in)
		if got != c.is {
			t.Fatalf("inferClusterPackageName(%s) expected %v got %v", c.in, c.is, got)
		}
	}
}

// TestIsInstalledNonName ensures non-name sources short-circuit to false.
func TestIsInstalledNonName(t *testing.T) {
	cl := New()
	ok, dep, err := cl.IsInstalled(context.Background(), "ghcr.io/org/pkg:1.0.0", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok || dep != nil {
		t.Fatalf("expected not installed for OCI ref")
	}
}

func TestNormalizeSource(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"ghcr.io/org/pkg:1.0.0", "oci://ghcr.io/org/pkg:1.0.0"},
		{"oci://ghcr.io/org/pkg:1.0.0", "oci://ghcr.io/org/pkg:1.0.0"},
		{"mypkg", "mypkg"},
	}
	for _, c := range cases {
		if got := normalizeSource(c.in); got != c.out {
			t.Fatalf("normalizeSource(%s) expected %s got %s", c.in, c.out, got)
		}
	}
}
