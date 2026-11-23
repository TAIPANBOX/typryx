package calibration_test

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	modulePath      = "github.com/TAIPANBOX/typryx"
	calibrationPath = modulePath + "/internal/calibration"
	commandPath     = modulePath + "/cmd/typryx"
)

// Structural half of CLAUDE.md invariant 25, "a calibration verdict is
// reported, never acted on": no package in this module but the command that
// prints the report can even name this package. The service, the API, the MCP
// surface, the backends and the wardryx proxy are where an answer is served or
// a decision is shaped, and none of them may import calibration, directly or
// through another package, so a drift verdict cannot become a refusal, a cap
// change or a signal there. Read from `go list`, which resolves the real
// import graph of the non-test files, so a comment or a string that names the
// package does not count.
func TestOnlyTheCommandCanSeeACalibrationVerdict(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Deps \" \"}}", modulePath+"/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	seen := map[string]bool{}
	commandSees := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg, deps := fields[0], fields[1:]
		seen[pkg] = true
		dependsOnCalibration := false
		for _, d := range deps {
			if d == calibrationPath {
				dependsOnCalibration = true
				break
			}
		}
		switch pkg {
		case calibrationPath:
			// The package itself.
		case commandPath:
			commandSees = dependsOnCalibration
		default:
			if dependsOnCalibration {
				t.Errorf("%s depends on internal/calibration, so a calibration verdict could be acted on there; only cmd/typryx may print one", pkg)
			}
		}
	}
	// Guards against a check that read nothing: the listing must carry the
	// packages this property is about, and the one allowed importer must
	// really import the package, or the absence everywhere else proves nothing.
	for _, must := range []string{calibrationPath, commandPath, modulePath + "/internal/service", modulePath + "/internal/wardryxproxy"} {
		if !seen[must] {
			t.Fatalf("go list did not list %s, so this measured nothing:\n%s", must, out)
		}
	}
	if !commandSees {
		t.Fatalf("cmd/typryx does not depend on internal/calibration, so the listing is not the graph this test expects:\n%s", out)
	}
}
