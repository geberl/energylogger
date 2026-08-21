package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"energylogger/internal/voltcraft"
)

// updateGolden regenerates testdata/golden from the current output. Run
// `go test -run TestRunGolden -update` after an intentional format change, then
// review the diff.
var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// testdataDir is the repository-wide testdata folder, two levels above this
// package.
var testdataDir = filepath.Join("..", "..", "testdata")

var outputFiles = []string{historyTextFile, historyCSVFile, statsTextFile}

// TestRunGolden decodes the real device captures in testdata and compares all
// three output files against the recorded expectations.
func TestRunGolden(t *testing.T) {
	outDir := t.TempDir()
	if code := run([]string{"-quiet", "-input", testdataDir, "-output", outDir}, io.Discard); code != 0 {
		t.Fatalf("run exited with code %d, want 0", code)
	}

	for _, name := range outputFiles {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("reading generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(testdataDir, "golden", name)
		if *updateGolden {
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("updating %s: %v", goldenPath, err)
			}
			t.Logf("updated %s", goldenPath)
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("reading %s: %v (run `go test -update` to create it)", goldenPath, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from the golden file:\n%s", name, firstDifference(string(want), string(got)))
		}
	}
}

// firstDifference describes where two multi-line strings start to disagree.
func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wantLines) && i < len(gotLines); i++ {
		if wantLines[i] != gotLines[i] {
			return "line " + itoa(i+1) + ":\n want: " + wantLines[i] + "\n  got: " + gotLines[i]
		}
	}
	return "line counts differ: want " + itoa(len(wantLines)) + ", got " + itoa(len(gotLines))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

// TestRunEnvironmentVariables checks that every folder and switch can be given
// through the environment instead of on the command line.
func TestRunEnvironmentVariables(t *testing.T) {
	outDir := t.TempDir()
	t.Setenv("ENERGYLOGGER_INPUT", testdataDir)
	t.Setenv("ENERGYLOGGER_OUTPUT", outDir)
	t.Setenv("ENERGYLOGGER_QUIET", "true")

	var sb strings.Builder
	if code := run(nil, &sb); code != 0 {
		t.Fatalf("run exited with code %d, want 0", code)
	}
	if sb.String() != "" {
		t.Errorf("ENERGYLOGGER_QUIET did not silence the output:\n%s", sb.String())
	}
	for _, name := range outputFiles {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
}

func TestRunFlagsWinOverEnvironmentVariables(t *testing.T) {
	outDir := t.TempDir()
	ignored := filepath.Join(outDir, "ignored")
	t.Setenv("ENERGYLOGGER_INPUT", testdataDir)
	t.Setenv("ENERGYLOGGER_OUTPUT", ignored)

	if code := run([]string{"-quiet", "-output", outDir}, io.Discard); code != 0 {
		t.Fatalf("run exited with code %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(outDir, statsTextFile)); err != nil {
		t.Errorf("output was not written to the folder given by -output: %v", err)
	}
	if _, err := os.Stat(ignored); err == nil {
		t.Error("the folder from ENERGYLOGGER_OUTPUT should have been ignored")
	}
}

// TestRunBadEnvironmentVariable covers the one parse failure that flag itself
// stays quiet about, so that the tool has to report it.
func TestRunBadEnvironmentVariable(t *testing.T) {
	t.Setenv("ENERGYLOGGER_QUIET", "notabool")

	var sb strings.Builder
	if code := run(nil, &sb); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(sb.String(), "ENERGYLOGGER_QUIET") {
		t.Errorf("expected the offending environment variable to be named, got:\n%s", sb.String())
	}
}

func TestRunUnexpectedArgument(t *testing.T) {
	var sb strings.Builder
	if code := run([]string{testdataDir}, &sb); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(sb.String(), "Unexpected argument") {
		t.Errorf("expected a complaint about the stray argument, got:\n%s", sb.String())
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "/?"} {
		var sb strings.Builder
		if code := run([]string{arg}, &sb); code != 0 {
			t.Errorf("%s: exit code = %d, want 0", arg, code)
		}
		if !strings.Contains(sb.String(), "Usage: energylogger") {
			t.Errorf("%s: expected usage output, got:\n%s", arg, sb.String())
		}
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var sb strings.Builder
	if code := run([]string{"-nope"}, &sb); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestRunNoValidFiles(t *testing.T) {
	inDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(inDir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if code := run([]string{"-input", inDir, "-output", t.TempDir()}, &sb); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(sb.String(), "No valid Voltcraft data files found.") {
		t.Errorf("expected the no-data message, got:\n%s", sb.String())
	}
	if !strings.Contains(sb.String(), "Invalid") {
		t.Errorf("expected the unparseable file to be reported, got:\n%s", sb.String())
	}
}

func TestRunEmptyInputFolder(t *testing.T) {
	var sb strings.Builder
	if code := run([]string{"-input", t.TempDir(), "-output", t.TempDir()}, &sb); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(sb.String(), "No valid Voltcraft data files found.") {
		t.Errorf("expected the no-data message, got:\n%s", sb.String())
	}
}

func TestRunMissingInputFolder(t *testing.T) {
	var sb strings.Builder
	code := run([]string{"-input", filepath.Join(t.TempDir(), "nope"), "-output", t.TempDir()}, &sb)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(sb.String(), "No valid Voltcraft data files found.") {
		t.Errorf("expected the no-data message, got:\n%s", sb.String())
	}
}

func TestRunUnusableOutputFolder(t *testing.T) {
	// A file where the output folder should be cannot be turned into a folder.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if code := run([]string{"-input", testdataDir, "-output", blocker}, &sb); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(sb.String(), "Failed to create folder") {
		t.Errorf("expected a folder creation failure, got:\n%s", sb.String())
	}
}

// TestRunIgnoresItsOwnOutput guards against the tool re-reading the files it
// wrote when the input and output folders are the same.
func TestRunIgnoresItsOwnOutput(t *testing.T) {
	dir := t.TempDir()
	capture, err := os.ReadFile(filepath.Join(testdataDir, "A04FC8D2.BIN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "A04FC8D2.BIN"), capture, 0o644); err != nil {
		t.Fatal(err)
	}

	first := map[string]string{}
	for pass := 1; pass <= 2; pass++ {
		var sb strings.Builder
		if code := run([]string{"-input", dir, "-output", dir}, &sb); code != 0 {
			t.Fatalf("pass %d exited with code %d, want 0", pass, code)
		}
		if strings.Contains(sb.String(), "Invalid") {
			t.Errorf("pass %d reported an invalid file, so it read its own output:\n%s", pass, sb.String())
		}
		for _, name := range outputFiles {
			content, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("pass %d: reading %s: %v", pass, name, err)
			}
			if pass == 1 {
				first[name] = string(content)
			} else if string(content) != first[name] {
				t.Errorf("%s changed between runs", name)
			}
		}
	}
}

func TestDedupeByTimestamp(t *testing.T) {
	events, err := voltcraft.ParseFile(filepath.Join(testdataDir, "A04FC8D2.BIN"))
	if err != nil {
		t.Fatal(err)
	}
	// Feeding the same capture in twice must collapse back to one copy.
	doubled := append(append([]voltcraft.PowerEvent{}, events...), events...)
	sortByTimestamp(doubled)
	deduped, stats := dedupeByTimestamp(doubled)
	if len(deduped) != len(events) {
		t.Errorf("got %d events after dedup, want %d", len(deduped), len(events))
	}
	for i := 1; i < len(deduped); i++ {
		if !deduped[i].Timestamp.After(deduped[i-1].Timestamp) {
			t.Fatalf("event %d is not strictly after its predecessor", i)
		}
	}
	if stats.Dropped != len(events) {
		t.Errorf("dropped = %d, want %d", stats.Dropped, len(events))
	}
	// Every copy matched the sample it duplicated, so nothing was really lost.
	if stats.Conflicting != 0 {
		t.Errorf("conflicting = %d, want 0", stats.Conflicting)
	}
}

// TestDedupeByTimestampCountsConflicts covers the case a re-dump does not
// explain: two samples claiming the same minute with different readings, as
// produced by mixing two devices' cards or winding the device clock back.
func TestDedupeByTimestampCountsConflicts(t *testing.T) {
	base := time.Date(2014, time.September, 11, 18, 43, 0, 0, time.UTC)
	events := []voltcraft.PowerEvent{
		{Timestamp: base, Voltage: 230, Current: 1, PowerFactor: 0.9},
		{Timestamp: base, Voltage: 230, Current: 1, PowerFactor: 0.9}, // a copy
		{Timestamp: base, Voltage: 224, Current: 1, PowerFactor: 0.9}, // a conflict
		{Timestamp: base.Add(time.Minute), Voltage: 230, Current: 1, PowerFactor: 0.9},
	}

	deduped, stats := dedupeByTimestamp(events)

	if len(deduped) != 2 {
		t.Errorf("got %d events after dedup, want 2", len(deduped))
	}
	if stats.Dropped != 2 {
		t.Errorf("dropped = %d, want 2", stats.Dropped)
	}
	if stats.Conflicting != 1 {
		t.Errorf("conflicting = %d, want 1", stats.Conflicting)
	}
	// The first of each run wins, so the kept sample is the original reading.
	if deduped[0].Voltage != 230 {
		t.Errorf("kept voltage = %v, want 230", deduped[0].Voltage)
	}
}
