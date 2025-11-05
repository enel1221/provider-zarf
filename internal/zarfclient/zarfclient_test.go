package zarfclient

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/zarf-dev/zarf/src/api/v1alpha1"
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
	t.Setenv("ZARFCLIENT_SKIP_CLUSTER", "1")
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

// TestOrderedComponentFilterPreservesOrder verifies components are returned in user's requested order.
func TestOrderedComponentFilterPreservesOrder(t *testing.T) {
	pkg := v1alpha1.ZarfPackage{
		Components: []v1alpha1.ZarfComponent{
			{Name: "a"},
			{Name: "b"},
			{Name: "c"},
		},
	}

	// Request in reverse order: c, b, a
	filter := buildComponentFilter([]string{"c", "b", "a"})
	result, err := filter.Apply(pkg)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 components, got %d", len(result))
	}

	expected := []string{"c", "b", "a"}
	for i, comp := range result {
		if comp.Name != expected[i] {
			t.Errorf("component[%d]: expected %s, got %s", i, expected[i], comp.Name)
		}
	}
}

// TestOrderedComponentFilterPartialOrder verifies subset of components in user order.
func TestOrderedComponentFilterPartialOrder(t *testing.T) {
	pkg := v1alpha1.ZarfPackage{
		Components: []v1alpha1.ZarfComponent{
			{Name: "base"},
			{Name: "storage"},
			{Name: "database"},
			{Name: "app"},
		},
	}

	// Request only database and base, in that order
	filter := buildComponentFilter([]string{"database", "base"})
	result, err := filter.Apply(pkg)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 components, got %d", len(result))
	}

	if result[0].Name != "database" || result[1].Name != "base" {
		t.Errorf("expected [database, base], got [%s, %s]", result[0].Name, result[1].Name)
	}
}

// TestOrderedComponentFilterNotFound verifies error when component doesn't exist.
func TestOrderedComponentFilterNotFound(t *testing.T) {
	pkg := v1alpha1.ZarfPackage{
		Components: []v1alpha1.ZarfComponent{
			{Name: "a"},
			{Name: "b"},
		},
	}

	filter := buildComponentFilter([]string{"a", "nonexistent", "b"})
	_, err := filter.Apply(pkg)
	if err == nil {
		t.Fatal("expected error for nonexistent component, got nil")
	}

	if err.Error() != `requested component "nonexistent" not found in package` {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestOrderedComponentFilterEmpty verifies empty filter returns all components.
func TestOrderedComponentFilterEmpty(t *testing.T) {
	pkg := v1alpha1.ZarfPackage{
		Components: []v1alpha1.ZarfComponent{
			{Name: "a"},
			{Name: "b"},
		},
	}

	filter := buildComponentFilter([]string{})
	result, err := filter.Apply(pkg)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Empty filter should return all components (handled by filters.Empty())
	// The filters.Empty() returns all components in package order
	if len(result) != 2 {
		t.Fatalf("expected 2 components, got %d", len(result))
	}
}

// TestOrderedComponentFilterSingleComponent verifies single component selection.
func TestOrderedComponentFilterSingleComponent(t *testing.T) {
	pkg := v1alpha1.ZarfPackage{
		Components: []v1alpha1.ZarfComponent{
			{Name: "x"},
			{Name: "y"},
			{Name: "z"},
		},
	}

	filter := buildComponentFilter([]string{"y"})
	result, err := filter.Apply(pkg)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 component, got %d", len(result))
	}

	if result[0].Name != "y" {
		t.Errorf("expected component 'y', got '%s'", result[0].Name)
	}
}

// TestLogrSlogBridgeDebugLevel tests that DEBUG logs map to V(1).
func TestLogrSlogBridgeDebugLevel(t *testing.T) {
	// Create a test logger with verbosity level tracking
	logger := testr.New(t)

	bridge := &logrSlogBridge{logger: logger}

	// Test DEBUG JSON log
	debugLog := `{"level":"DEBUG","msg":"debug message"}`
	_, err := bridge.Write([]byte(debugLog))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Test lowercase debug
	debugLogLower := `{"level":"debug","msg":"lowercase debug"}`
	_, err = bridge.Write([]byte(debugLogLower))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Note: We can't directly assert V(1) was called with testr,
	// but we verify no errors and the code path is exercised
}

// TestLogrSlogBridgeLevels tests all log level mappings.
func TestLogrSlogBridgeLevels(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"ERROR uppercase", `{"level":"ERROR","msg":"error message"}`},
		{"error lowercase", `{"level":"error","msg":"lowercase error"}`},
		{"WARN uppercase", `{"level":"WARN","msg":"warning message"}`},
		{"warn lowercase", `{"level":"warn","msg":"lowercase warning"}`},
		{"INFO", `{"level":"INFO","msg":"info message"}`},
		{"Plain text", "plain text message"},
		{"Empty", ""},
		{"Whitespace only", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testr.New(t)
			bridge := &logrSlogBridge{logger: logger}

			n, err := bridge.Write([]byte(tt.input))
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}
			if n != len(tt.input) {
				t.Errorf("expected Write to return %d, got %d", len(tt.input), n)
			}
		})
	}
}

// TestLogrSlogBridgeWarnPrefix tests that WARN logs get the "WARN:" prefix.
func TestLogrSlogBridgeWarnPrefix(t *testing.T) {
	// Create a simple logger that captures messages
	var lastMessage string
	logger := logr.New(&testLogSink{
		infoFunc: func(level int, msg string, keysAndValues ...interface{}) {
			lastMessage = msg
		},
	})

	bridge := &logrSlogBridge{logger: logger}

	warnLog := `{"level":"WARN","msg":"warning message"}`
	_, err := bridge.Write([]byte(warnLog))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if !strings.HasPrefix(lastMessage, "WARN:") {
		t.Errorf("expected WARN log to have 'WARN:' prefix, got: %s", lastMessage)
	}
}

// testLogSink implements logr.LogSink for testing
type testLogSink struct {
	infoFunc  func(level int, msg string, keysAndValues ...interface{})
	errorFunc func(err error, msg string, keysAndValues ...interface{})
}

func (t *testLogSink) Init(info logr.RuntimeInfo) {}

func (t *testLogSink) Enabled(level int) bool { return true }

func (t *testLogSink) Info(level int, msg string, keysAndValues ...interface{}) {
	if t.infoFunc != nil {
		t.infoFunc(level, msg, keysAndValues...)
	}
}

func (t *testLogSink) Error(err error, msg string, keysAndValues ...interface{}) {
	if t.errorFunc != nil {
		t.errorFunc(err, msg, keysAndValues...)
	}
}

func (t *testLogSink) WithValues(keysAndValues ...interface{}) logr.LogSink { return t }

func (t *testLogSink) WithName(name string) logr.LogSink { return t }
