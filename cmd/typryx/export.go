package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/TAIPANBOX/typryx/internal/traininglog"
)

// exportCmd implements:
//
//	typryx export --training [--training-dir DIR] [--ledger DIR] [--out FILE]
//
// It joins the opt-in training log with the human truths posted to
// /v1/outcome and writes one JSONL row per answer that has a truth:
// {template, template_version, type, state, label}. The label is the human
// truth and nothing else; a backend's answer is never read (see
// internal/traininglog.Export for the join rules).
//
// Read-only on both directories. Rows go to --out (created 0600) or stdout;
// the per-template counts always go to stderr, so stdout stays pure JSONL.
// Exit 0 on success, 2 on a usage or configuration error.
func exportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	training := fs.Bool("training", false, "export the training set: states paired with human truths (required; the only export there is)")
	trainingDir := fs.String("training-dir", "", "directory holding training.ndjson (default: $TYPRYX_TRAINING_DIR)")
	ledgerDir := fs.String("ledger", "", "directory holding outcomes.ndjson (default: $TYPRYX_LEDGER_DIR)")
	outPath := fs.String("out", "", "write the rows to this file, created 0600, instead of stdout; must not be inside either input directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "typryx export: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if !*training {
		fmt.Fprintln(stderr, "typryx export: name what to export. The only export is --training.")
		return 2
	}
	tdir := *trainingDir
	if tdir == "" {
		tdir = os.Getenv("TYPRYX_TRAINING_DIR")
	}
	if tdir == "" {
		fmt.Fprintln(stderr, "typryx export: no training directory given. Set --training-dir DIR or TYPRYX_TRAINING_DIR.")
		return 2
	}
	ldir := *ledgerDir
	if ldir == "" {
		ldir = os.Getenv("TYPRYX_LEDGER_DIR")
	}
	if ldir == "" {
		fmt.Fprintln(stderr, "typryx export: no ledger directory given (the human truths live there). Set --ledger DIR or TYPRYX_LEDGER_DIR.")
		return 2
	}

	out := stdout
	if *outPath != "" {
		if inside, which := pathInside(*outPath, tdir, ldir); inside {
			fmt.Fprintf(stderr, "typryx export: --out %s is inside %s; the export is read-only on both directories. Write it elsewhere.\n", *outPath, which)
			return 2
		}
		// The file holds a customer's own data, so 0600, like the log it
		// comes from. The path is the operator's own flag.
		f, err := os.OpenFile(*outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 G302 G703 -- an operator-provided path from --out
		if err != nil {
			fmt.Fprintf(stderr, "typryx export: opening --out: %v\n", err)
			return 2
		}
		defer f.Close()
		out = f
	}

	rep, err := traininglog.Export(traininglog.ExportOptions{TrainingDir: tdir, LedgerDir: ldir}, out)
	if err != nil {
		fmt.Fprintf(stderr, "typryx export: %s\n", err.Error())
		return 2
	}
	printExportCounts(stderr, rep)
	return 0
}

func printExportCounts(w io.Writer, rep traininglog.ExportReport) {
	names := make([]string, 0, len(rep.Templates))
	for n := range rep.Templates {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintln(w, "no training lines")
	}
	for _, n := range names {
		c := rep.Templates[n]
		fmt.Fprintf(w, "%s exported=%d skipped_no_truth=%d skipped_version_mismatch=%d skipped_bad_truth=%d skipped_duplicate_id=%d\n",
			n, c.Exported, c.SkippedNoTruth, c.SkippedVersionMismatch, c.SkippedBadTruth, c.SkippedDuplicateID)
	}
	fmt.Fprintf(w, "malformed_lines=%d torn_lines=%d orphan_truth=%d\n", rep.MalformedLines, rep.TornLines, rep.OrphanTruth)
}

// pathInside reports whether path lies within any of dirs, and which. Both
// sides are made absolute and cleaned; symlinks are resolved where the path
// exists, so a link into an input directory is caught too.
func pathInside(path string, dirs ...string) (bool, string) {
	p := resolve(path)
	for _, d := range dirs {
		dd := resolve(d)
		rel, err := filepath.Rel(dd, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true, d
		}
	}
	return false, ""
}

// resolve makes p absolute and clean, resolving symlinks in the longest
// existing prefix (the file itself usually does not exist yet).
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	existing, rest := abs, ""
	for {
		if real, err := filepath.EvalSymlinks(existing); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return abs
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}
