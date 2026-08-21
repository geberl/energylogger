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
	"sort"
	"time"

	"energylogger/internal/export"
	"energylogger/internal/voltcraft"
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
	for _, arg := range args {
		if arg == "/?" {
			fmt.Fprintln(stdout, banner)
			usage(stdout)
			return 0
		}
	}

	fs := flag.NewFlagSet("energylogger", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() { usage(stdout) }
	var (
		inputDir  = fs.String("input", ".", "directory to read Voltcraft .BIN files from")
		outputDir = fs.String("output", ".", "directory to write the history and statistics files to")
		quiet     = fs.Bool("quiet", false, "suppress the banner and per-file progress output")
		noColor   = fs.Bool("no-color", false, "disable ANSI colour in progress output")
	)
	if err := fs.Parse(args); err != nil {
		// flag has already reported the problem and printed the usage.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Positional arguments stay supported for compatibility with the original
	// tool, but an explicit flag wins.
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	positional := fs.Args()
	if len(positional) > 2 {
		fmt.Fprintf(stdout, "Too many arguments: expected at most <input folder> <output folder>.\n")
		usage(stdout)
		return 2
	}
	if len(positional) > 0 && !explicit["input"] {
		*inputDir = positional[0]
	}
	if len(positional) > 1 && !explicit["output"] {
		*outputDir = positional[1]
	}

	col := palette{enabled: colorEnabled(*noColor, stdout)}
	log := &progress{w: stdout, quiet: *quiet}

	log.line(banner)

	if err := os.MkdirAll(*outputDir, 0o755); err != nil {
		fmt.Fprintf(stdout, "%s %s: %v\n", col.red("Failed to create folder"), *outputDir, err)
		return 1
	}
	log.linef("Reading data files from folder '%s'.", col.brightWhite(*inputDir))
	log.linef("Writing statistics to folder '%s'.", col.brightWhite(*outputDir))

	started := time.Now()

	targets := map[string]string{
		historyTextFile: filepath.Join(*outputDir, historyTextFile),
		historyCSVFile:  filepath.Join(*outputDir, historyCSVFile),
		statsTextFile:   filepath.Join(*outputDir, statsTextFile),
	}

	files, err := inputFiles(*inputDir, targets)
	if err != nil {
		fmt.Fprintf(stdout, "%s '%s': %v\n", col.red("Failed to read folder"), *inputDir, err)
		return 1
	}

	var (
		events    []voltcraft.PowerEvent
		fileCount int
	)
	for _, file := range files {
		log.stepf("Processing file: %s...", file)
		raw, err := os.ReadFile(file)
		if err != nil {
			log.done(col.red("Failed to open"))
			continue
		}
		parsed, err := voltcraft.ParseBytes(raw)
		if err != nil {
			// Only the "not a Voltcraft file" case is routine; anything else
			// says something about the file worth showing.
			if errors.Is(err, voltcraft.ErrNotVoltcraftFile) {
				log.done(col.red("Invalid"))
			} else {
				log.done(col.red("Invalid") + ": " + err.Error())
			}
			continue
		}
		events = append(events, parsed...)
		fileCount++
		log.done(col.green("Ok"))
	}

	if len(events) == 0 {
		log.line(col.yellow("No valid Voltcraft data files found."))
		log.line(col.green("Finished."))
		return 0
	}

	// Blackout detection and daily grouping both need the samples in
	// chronological order, merged across all input files.
	log.stepf("Sorting power data...")
	sortByTimestamp(events)
	log.done(col.green("Done"))

	// The same data dumped to the SD card twice yields duplicate samples.
	log.stepf("Removing duplicates from power data...")
	events, duplicates := dedupeByTimestamp(events)
	if duplicates.Dropped == 0 {
		log.done(col.green("Done"))
	} else {
		log.done(col.green("Done") + fmt.Sprintf(" (dropped %d duplicate samples)", duplicates.Dropped))
	}
	// A duplicate timestamp whose readings disagree is not a re-dump of data
	// already held: one of the two measurements is gone from every total below.
	if duplicates.Conflicting > 0 {
		log.line(col.yellow(fmt.Sprintf(
			"Warning: %d of them carried different readings and were discarded anyway.",
			duplicates.Conflicting)))
		log.line(col.yellow(
			"  This happens when cards from two devices share an input folder, or when the " +
				"device clock was wound back; the statistics below are missing those samples."))
	}

	exitCode := 0
	writeStep := func(name string, write func() error) {
		log.stepf("Saving %s...", col.brightWhite(name))
		if err := write(); err != nil {
			log.done(col.red("Failed") + ": " + err.Error())
			exitCode = 1
			return
		}
		log.done(col.green("Ok"))
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

	if fileCount > 0 {
		log.linef("Processed %d files in %s.", fileCount, time.Since(started).Round(time.Millisecond))
	}
	log.line(col.green("Finished."))
	return exitCode
}

// inputFiles lists the candidate data files in dir, skipping subdirectories and
// the tool's own output files so that a second run over the same folder does not
// try to parse them.
func inputFiles(dir string, targets map[string]string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, target := range targets {
		if abs, err := filepath.Abs(target); err == nil {
			skip[abs] = true
		}
	}

	var files []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		if abs, err := filepath.Abs(match); err == nil && skip[abs] {
			continue
		}
		files = append(files, match)
	}
	return files, nil
}

// sortByTimestamp orders the samples chronologically, keeping samples that
// share a timestamp in the order their files were read.
func sortByTimestamp(events []voltcraft.PowerEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
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
	fmt.Fprint(w, `
Usage: energylogger [flags] [<input folder>] [<output folder>]

Decodes every Voltcraft Energy-Logger 4000 data file in the input folder and
writes these files into the output folder:

  `+historyTextFile+`    minute-by-minute parameter history, human readable
  `+historyCSVFile+`    the same history as CSV, at full precision
  `+statsTextFile+`      overall, daily and blackout statistics

Both folders default to the current directory. The folders may be given either
positionally or with the flags below.

Flags:
  -input string     directory to read Voltcraft .BIN files from (default ".")
  -output string    directory to write the output files to (default ".")
  -quiet            suppress the banner and per-file progress output
  -no-color         disable ANSI colour in progress output
  -h, --help, /?    show this help

Examples:
  energylogger /Volumes/SDCARD ~/energy      read from the SD card, write to ~/energy
  energylogger /Volumes/SDCARD               write to the current directory
  energylogger                               read and write in the current directory
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
	fmt.Fprintln(p.w, s)
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
	fmt.Fprintf(p.w, format, a...)
}

func (p *progress) done(status string) {
	if p.quiet {
		return
	}
	fmt.Fprintf(p.w, " %s\n", status)
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
