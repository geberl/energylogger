// Command energylogger decodes the .BIN files written by a Voltcraft
// Energy-Logger 4000 to its SD card and writes a minute-by-minute parameter
// history plus daily, overall and blackout statistics.
//
// It is a Go port of https://github.com/vbocan/voltcraft-energy-analyzer by
// Valer Bocan (MIT licensed).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v3"

	"github.com/geberl/energylogger/internal/export"
	"github.com/geberl/energylogger/internal/voltcraft"
)

const (
	applicationName = "energylogger"
	envVarPrefix    = "ENERGYLOGGER"
)

// Names of the files written into the output directory.
const (
	historyTextFile = "voltcraft_history.txt"
	historyCSVFile  = "voltcraft_history.csv"
	statsTextFile   = "voltcraft_stats.txt"
)

const banner = "Analyzer for Voltcraft Energy Logger 4000 - v1.0 " +
	"(Go port of vbocan/voltcraft-energy-analyzer)"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run executes the tool and returns the process exit code.
func run(args []string, stdout io.Writer) int {
	// The original tool accepted /? as a help switch; flag does not know it.
	if slices.Contains(args, "/?") {
		_, _ = fmt.Fprintln(stdout, banner)
		usage(stdout)
		return 0
	}

	fs := flag.NewFlagSet(applicationName, flag.ContinueOnError)
	fs.SetOutput(stdout)
	// reported tells a bad flag, which flag itself complains about, from a bad
	// environment variable, which only ff notices.
	var reported bool
	fs.Usage = func() { reported = true; usage(stdout) }
	var (
		inputDir  = fs.String("input", ".", "directory to read Voltcraft .BIN files from")
		outputDir = fs.String("output", ".", "directory to write the history and statistics files to")
		quiet     = fs.Bool("quiet", false, "suppress the banner and per-file progress output")
		noColor   = fs.Bool("no-color", false, "disable ANSI colour in progress output")
	)
	if err := ff.Parse(fs, args, ff.WithEnvVarPrefix(envVarPrefix)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if !reported {
			_, _ = fmt.Fprintf(stdout, "%v\n", err)
			usage(stdout)
		}
		return 2
	}

	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stdout,
			"Unexpected argument %q: give the folders with -input and -output.\n", fs.Arg(0))
		usage(stdout)
		return 2
	}

	col := palette{enabled: colorEnabled(*noColor, stdout)}
	out := &progress{w: stdout, quiet: *quiet}

	out.line(banner)

	out.linef("Reading data files from folder '%s'.", col.brightWhite(*inputDir))

	started := time.Now()

	targets := map[string]string{
		historyTextFile: filepath.Join(*outputDir, historyTextFile),
		historyCSVFile:  filepath.Join(*outputDir, historyCSVFile),
		statsTextFile:   filepath.Join(*outputDir, statsTextFile),
	}

	// Read the input folder before creating the output folder, so that a typo'd
	// -input does not leave an empty directory behind.
	files, err := inputFiles(*inputDir, targets)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "%s '%s': %v\n", col.red("Failed to read folder"), *inputDir, err)
		if errors.Is(err, os.ErrNotExist) {
			_, _ = fmt.Fprintln(stdout, "  Check the path, and that the card is mounted.")
		}
		return 1
	}

	out.linef("Writing statistics to folder '%s'.", col.brightWhite(*outputDir))

	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		_, _ = fmt.Fprintf(stdout, "%s %s: %v\n", col.red("Failed to create folder"), *outputDir, err)
		return 1
	}

	var (
		events    []voltcraft.PowerEvent
		fileCount int
	)
	for _, file := range files {
		out.stepf("Processing file: %s...", file)
		raw, err := os.ReadFile(file)
		if err != nil {
			// Include the error, as the parse failure below does: a permissions
			// problem otherwise reads exactly like a file that went away.
			out.done(col.red("Failed to open") + ": " + err.Error())
			continue
		}
		parsed, err := voltcraft.ParseBytes(raw)
		if err != nil {
			// Only the "not a Voltcraft file" case is routine; anything else
			// says something about the file worth showing.
			if errors.Is(err, voltcraft.ErrNotVoltcraftFile) {
				out.done(col.red("Invalid"))
			} else {
				out.done(col.red("Invalid") + ": " + err.Error())
			}
			continue
		}
		events = append(events, parsed...)
		fileCount++
		out.done(col.green("Ok"))
	}

	if len(events) == 0 {
		out.line(col.yellow("No valid Voltcraft data files found."))
		out.line(col.green("Finished."))
		return 0
	}

	// Blackout detection and daily grouping both need the samples in
	// chronological order, merged across all input files.
	out.stepf("Sorting power data...")
	sortByTimestamp(events)
	out.done(col.green("Done"))

	// The same data dumped to the SD card twice yields duplicate samples.
	out.stepf("Removing duplicates from power data...")
	events, duplicates := dedupeByTimestamp(events)
	if duplicates.Dropped == 0 {
		out.done(col.green("Done"))
	} else {
		out.done(col.green("Done") + fmt.Sprintf(" (dropped %d duplicate samples)", duplicates.Dropped))
	}
	// A duplicate timestamp whose readings disagree is not a re-dump of data
	// already held: one of the two measurements is gone from every total below.
	if duplicates.Conflicting > 0 {
		out.line(col.yellow(fmt.Sprintf(
			"Warning: %d of them carried different readings and were discarded anyway.",
			duplicates.Conflicting)))
		out.line(col.yellow(
			"  This happens when cards from two devices share an input folder, or when the " +
				"device clock was wound back; the statistics below are missing those samples."))
	}

	exitCode := 0
	writeStep := func(name string, write func() error) {
		out.stepf("Saving %s...", col.brightWhite(name))
		if err := write(); err != nil {
			out.done(col.red("Failed") + ": " + err.Error())
			exitCode = 1
			return
		}
		out.done(col.green("Ok"))
	}

	writeStep(historyTextFile, func() error {
		return export.WriteHistoryTextFile(targets[historyTextFile], events)
	})
	writeStep(historyCSVFile, func() error {
		return export.WriteHistoryCSVFile(targets[historyCSVFile], events)
	})

	stats := voltcraft.NewStatistics(events)
	writeStep(statsTextFile, func() error {
		return export.WriteStatisticsFile(targets[statsTextFile],
			stats.Overall(), stats.Daily(), stats.Blackouts())
	})

	// At least one file parsed, or the len(events) == 0 return above would have
	// been taken.
	out.linef("Processed %d files in %s.", fileCount, time.Since(started).Round(time.Millisecond))
	out.line(col.green("Finished."))
	return exitCode
}

// inputFiles lists the candidate data files in dir, skipping subdirectories,
// dotfiles, and the tool's own output files so that a second run over the same
// folder does not try to parse them.
//
// A folder that is not there is an error rather than an empty result. The input
// is usually a removable card, so a path with nothing at it means a typo or an
// unmounted card far more often than it means an empty folder, and reporting
// success for that is the one failure this tool must not have. filepath.Glob
// could not tell the two apart: it returns no matches and no error for both.
func inputFiles(dir string, targets map[string]string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	skip := make(map[string]bool, len(targets))
	for _, target := range targets {
		abs, err := filepath.Abs(target)
		if err != nil {
			// Only reachable when the process has no working directory, which
			// is not a per-file problem.
			return nil, fmt.Errorf("resolving output path %s: %w", target, err)
		}
		skip[abs] = true
	}

	var files []string
	for _, entry := range entries {
		name := entry.Name()
		// filepath.Match has no shell-style dotfile exemption, so the old "*"
		// glob picked up .DS_Store and friends. They are never data files, and
		// reporting them as Invalid looks like a malfunction.
		if strings.HasPrefix(name, ".") {
			continue
		}
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, name)
		// DirEntry.IsDir is false for a symlink to a directory, which the
		// os.Stat this loop used to do would skip. Follow the link for that
		// case alone, so the common case stays one readdir. A link that cannot
		// be followed is kept: the read below then names it as a failure
		// instead of dropping it silently.
		if entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				continue
			}
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", path, err)
		}
		if skip[abs] {
			continue
		}
		files = append(files, path)
	}
	return files, nil
}

// sortByTimestamp orders the samples chronologically, keeping samples that
// share a timestamp in the order their files were read.
func sortByTimestamp(events []voltcraft.PowerEvent) {
	slices.SortStableFunc(events, func(a, b voltcraft.PowerEvent) int {
		return a.Timestamp.Compare(b.Timestamp)
	})
}

// dedupeStats counts what dedupeByTimestamp discarded.
type dedupeStats struct {
	// Dropped is the number of samples removed.
	Dropped int

	// Conflicting is the subset of those whose readings differed from the
	// sample kept in their place, so a real measurement was lost rather than a
	// copy of one already held.
	Conflicting int
}

// dedupeByTimestamp drops samples that repeat the timestamp of the one before,
// keeping the first of each run. The events must already be sorted.
//
// Repeats are expected: dumping the same SD card twice yields the whole data
// set again. Those copies are identical and dropping them is free. A repeat
// carrying different readings is another matter — it means two devices' cards
// were mixed into one input folder, or the device clock was wound back an hour
// — so it is counted separately for the caller to report.
func dedupeByTimestamp(events []voltcraft.PowerEvent) ([]voltcraft.PowerEvent, dedupeStats) {
	var stats dedupeStats
	if len(events) == 0 {
		return events, stats
	}
	kept := events[:1]
	for _, e := range events[1:] {
		previous := kept[len(kept)-1]
		if e.Timestamp.Equal(previous.Timestamp) {
			stats.Dropped++
			if !sameReadings(e, previous) {
				stats.Conflicting++
			}
			continue
		}
		kept = append(kept, e)
	}
	return kept, stats
}

// sameReadings reports whether two samples measured the same thing. Only the
// three decoded fields are compared; active and apparent power are derived from
// them. The values come from integers divided by a fixed constant, so they
// compare exactly.
func sameReadings(a, b voltcraft.PowerEvent) bool {
	return a.Voltage == b.Voltage && a.Current == b.Current && a.PowerFactor == b.PowerFactor
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `
Usage: energylogger [flags]

Decodes every Voltcraft Energy-Logger 4000 data file in the input folder and
writes these files into the output folder:

  `+historyTextFile+`    minute-by-minute parameter history, human readable
  `+historyCSVFile+`    the same history as CSV, at full precision
  `+statsTextFile+`      overall, daily and blackout statistics

Both folders default to the current directory.

Flags:
  -input string     directory to read Voltcraft .BIN files from (default ".")
  -output string    directory to write the output files to (default ".")
  -quiet            suppress the banner and per-file progress output
  -no-color         disable ANSI colour in progress output
  -h, --help, /?    show this help

Every flag can also be given as an environment variable, which a flag on the
command line overrides:

  ENERGYLOGGER_INPUT, ENERGYLOGGER_OUTPUT, ENERGYLOGGER_QUIET,
  ENERGYLOGGER_NO_COLOR

Examples:
  energylogger -input /Volumes/SDCARD -output ~/energy   read the SD card, write to ~/energy
  energylogger -input /Volumes/SDCARD                    write to the current directory
  energylogger                                           read and write in the current directory
`)
}

// progress writes the tool's running commentary, which -quiet silences.
type progress struct {
	w     io.Writer
	quiet bool
}

// line writes a complete line.
func (p *progress) line(s string) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintln(p.w, s)
}

func (p *progress) linef(format string, a ...any) {
	p.line(fmt.Sprintf(format, a...))
}

// stepf starts a line that a later done call completes, so that the outcome of
// a step appears on the same line as its description.
func (p *progress) stepf(format string, a ...any) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintf(p.w, format, a...)
}

func (p *progress) done(status string) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintf(p.w, " %s\n", status)
}

// palette colours terminal output. With enabled false every method returns its
// argument unchanged.
type palette struct {
	enabled bool
}

func (p palette) wrap(code, s string) string {
	if !p.enabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) red(s string) string         { return p.wrap("31", s) }
func (p palette) green(s string) string       { return p.wrap("32", s) }
func (p palette) yellow(s string) string      { return p.wrap("33", s) }
func (p palette) brightWhite(s string) string { return p.wrap("97", s) }

// colorEnabled reports whether to emit ANSI escapes: only for a terminal, and
// not when the user or the environment asked otherwise.
func colorEnabled(noColor bool, w io.Writer) bool {
	if noColor {
		return false
	}
	// https://no-color.org: any non-empty value disables colour.
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
