package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/TAIPANBOX/typryx/internal/calibration"
	"github.com/TAIPANBOX/typryx/internal/door"
	"github.com/TAIPANBOX/typryx/internal/record"
)

const defaultMinN = 30

// calibrationCmd implements:
//
//	typryx calibration [--ledger DIR] [--min-n 30] [--max-brier X]
//	  [--max-ece X] [--json] [--emit PATH --agent-id agent://...]
//
// It is read-only over the ledger (see internal/calibration's package doc):
// the service may still be running and appending to it. Exit 0 when no group
// drifted, 1 when at least one did, 2 on a usage or configuration error.
func calibrationCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("calibration", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ledgerDir := fs.String("ledger", "", "directory holding answers.ndjson and outcomes.ndjson (default: $TYPRYX_LEDGER_DIR)")
	minN := fs.Int("min-n", defaultMinN, "minimum scored items a group needs before it gets a verdict")
	maxBrier := fs.Float64("max-brier", -1, "drift bound on the multi-class Brier score (unset: no bound)")
	maxECE := fs.Float64("max-ece", -1, "drift bound on ECE (unset: no bound)")
	asJSON := fs.Bool("json", false, "emit JSON instead of a text table")
	emitPath := fs.String("emit", "", "write one calibration_drift event per drift group to this agent-event file")
	agentID := fs.String("agent-id", "", "the agent:// identity calibration_drift events are recorded under (required for --emit to write anything)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "typryx calibration: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	dir := *ledgerDir
	if dir == "" {
		dir = os.Getenv("TYPRYX_LEDGER_DIR")
	}
	if dir == "" {
		fmt.Fprintln(stderr, "typryx calibration: no ledger directory given. Set --ledger DIR or TYPRYX_LEDGER_DIR.")
		return 2
	}

	opts := calibration.Options{LedgerDir: dir, MinN: *minN}
	if *maxBrier >= 0 {
		v := *maxBrier
		opts.MaxBrier = &v
	}
	if *maxECE >= 0 {
		v := *maxECE
		opts.MaxECE = &v
	}

	if *emitPath != "" && *agentID != "" && !door.ValidIdentity(*agentID) {
		fmt.Fprintf(stderr, "typryx calibration: --agent-id %q is not a well-formed agent:// identity (need agent://<host>/<path>)\n", *agentID)
		return 2
	}

	report, err := calibration.Run(opts)
	if err != nil {
		fmt.Fprintf(stderr, "typryx calibration: %s\n", err.Error())
		return 2
	}

	if *emitPath != "" {
		emitDrift(report, *emitPath, *agentID, stderr)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "typryx calibration: encoding JSON: %v\n", err)
			return 2
		}
	} else {
		printText(stdout, report)
	}

	if report.AnyDrift() {
		return 1
	}
	return 0
}

func printText(stdout io.Writer, report calibration.Report) {
	if len(report.Groups) == 0 {
		fmt.Fprintln(stdout, "no scored groups")
	}
	for _, g := range report.Groups {
		fmt.Fprintln(stdout, calibration.FormatGroupLine(g))
	}
	c := report.Counters
	fmt.Fprintf(stdout, "not_scorable=%d orphan_outcome=%d truth_not_a_key=%d malformed_lines=%d torn_lines=%d\n",
		c.NotScorable, c.OrphanOutcome, c.TruthNotAKey, c.MalformedLines, c.TornLines)
}

// emitDrift writes one calibration_drift event per drift group to path.
// Without a valid --agent-id, every event is skipped and counted by
// internal/record itself (SPEC 6.1 forbids a fabricated agent_id); this
// function's job is only to say so on stderr, once, rather than leave the
// operator to notice an empty event file.
func emitDrift(report calibration.Report, path, agentID string, stderr io.Writer) {
	j, err := record.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "typryx calibration: --emit could not open %s: %v\n", path, err)
		return
	}
	defer j.Close()

	var written, skipped int
	for _, g := range report.Groups {
		if g.Verdict != "drift" {
			continue
		}
		outcome := j.CalibrationDrift(agentID, record.CalibrationDriftData{
			Template: g.Template, TemplateVersion: g.TemplateVersion, Backend: g.Backend, Model: g.Model,
			N: g.N, Accuracy: g.Accuracy, MeanConfidence: g.MeanConfidence, Brier: g.Brier, ECE: g.ECE,
			BoundsCrossed: g.BoundsCrossed,
		})
		switch outcome {
		case record.Written:
			written++
		case record.SkippedNoAgentID:
			skipped++
		}
	}
	if skipped > 0 {
		fmt.Fprintf(stderr, "typryx calibration: --agent-id not set (or empty); %d calibration_drift event(s) skipped and counted, none written under a fabricated identity (SPEC 6.1)\n", skipped)
	}
	if written > 0 {
		fmt.Fprintf(stderr, "typryx calibration: wrote %d calibration_drift event(s) to %s\n", written, path)
	}
}
