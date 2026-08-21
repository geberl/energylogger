// Package export writes decoded Voltcraft samples and their statistics to
// plain text and CSV files.
package export

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/geberl/energylogger/internal/voltcraft"
)

// Timestamp layouts used throughout the output files.
const (
	tsBracketed   = "[2006-01-02 15:04]"
	tsPlain       = "2006-01-02 15:04"
	dateBracketed = "[2006-01-02]"
)

// WriteHistoryTextFile writes the minute-by-minute parameter history to a text
// file.
func WriteHistoryTextFile(path string, events []voltcraft.PowerEvent) error {
	return writeFile(path, func(w io.Writer) error {
		return WriteHistoryText(w, events)
	})
}

// WriteHistoryCSVFile writes the minute-by-minute parameter history to a CSV
// file.
func WriteHistoryCSVFile(path string, events []voltcraft.PowerEvent) error {
	return writeFile(path, func(w io.Writer) error {
		return WriteHistoryCSV(w, events)
	})
}

// WriteStatisticsFile writes the overall, daily and blackout statistics to a
// text file.
func WriteStatisticsFile(path string, overall voltcraft.OverallInfo, daily []voltcraft.DailyInfo, blackouts voltcraft.BlackoutInfo) error {
	return writeFile(path, func(w io.Writer) error {
		return WriteStatistics(w, overall, daily, blackouts)
	})
}

// WriteHistoryText writes the parameter history as human-readable lines.
func WriteHistoryText(w io.Writer, events []voltcraft.PowerEvent) error {
	out := &errWriter{w: w}
	out.printf("== PARAMETER HISTORY ==\n")
	out.printf("\n")
	for _, e := range events {
		out.printf("%s U=%.1fV I=%.3fA cosPHI=%.2f P=%.3fkW S=%.3fkVA\n",
			e.Timestamp.Format(tsBracketed), e.Voltage, e.Current, e.PowerFactor, e.Power, e.ApparentPower)
	}
	return out.err
}

// WriteHistoryCSV writes the parameter history as CSV. Numbers are written at
// full precision, unrounded, for further processing in a spreadsheet.
func WriteHistoryCSV(w io.Writer, events []voltcraft.PowerEvent) error {
	cw := csv.NewWriter(w)

	// The timestamp column is named after what it holds: the device's own clock
	// reading, with no timezone to convert from. Spreadsheets and importers
	// otherwise tend to reinterpret a bare "Timestamp" in the reader's zone.
	header := []string{"Timestamp (device local time)", "Voltage (V)", "Current (A)", "cosPHI", "Active Power (kW)", "Apparent Power (kVA)"}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, e := range events {
		record := []string{
			e.Timestamp.Format(tsPlain),
			formatFloat(e.Voltage),
			formatFloat(e.Current),
			formatFloat(e.PowerFactor),
			formatFloat(e.Power),
			formatFloat(e.ApparentPower),
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteStatistics writes the three statistics sections: the whole interval, one
// entry per day, and the detected blackouts.
func WriteStatistics(w io.Writer, overall voltcraft.OverallInfo, daily []voltcraft.DailyInfo, blackouts voltcraft.BlackoutInfo) error {
	out := &errWriter{w: w}

	out.printf("==== OVERALL STATISTICS ==================\n")
	out.printf("Interval: %s-%s (%s)\n",
		overall.Start.Format(tsBracketed),
		overall.End.Format(tsBracketed),
		FormatDuration(overall.End.Sub(overall.Start)))
	if d := overall.AvgDailyConsumption; d != nil {
		out.printf("Average consumption: %.2fkWh/day | Projected: %.2fkWh/month or %.2fkWh/year.\n",
			*d, *d*30.0, *d*365.0)
	}
	out.printf("\n")

	out.printf("- ACTIVE POWER\n")
	out.printf("Total energy consumption: %.2fkWh.\n", overall.Stats.TotalActivePower)
	out.printf("Peak power was %.2fkW and occurred on %s.\n",
		overall.Stats.MaxActivePower.Power,
		overall.Stats.MaxActivePower.Timestamp.Format(tsBracketed))
	out.printf("Minute by minute average power: %.2fkW.\n", overall.Stats.AvgActivePower)
	out.printf("\n")

	out.printf("- APPARENT POWER\n")
	out.printf("Total energy consumption: %.2fkVAh.\n", overall.Stats.TotalApparentPower)
	out.printf("Peak power was %.2fkVA and occurred on %s.\n",
		overall.Stats.MaxApparentPower.ApparentPower,
		overall.Stats.MaxApparentPower.Timestamp.Format(tsBracketed))
	out.printf("Minute by minute average power: %.2fkVA.\n", overall.Stats.AvgApparentPower)
	out.printf("\n")

	out.printf("- VOLTAGE\n")
	out.printf("Minimum voltage was %.1fV and occurred on %s.\n",
		overall.Stats.MinVoltage.Voltage,
		overall.Stats.MinVoltage.Timestamp.Format(tsBracketed))
	out.printf("Maximum voltage was %.1fV and occurred on %s.\n",
		overall.Stats.MaxVoltage.Voltage,
		overall.Stats.MaxVoltage.Timestamp.Format(tsBracketed))
	out.printf("Minute by minute average voltage: %.1fV.\n", overall.Stats.AvgVoltage)
	out.printf("\n")
	out.printf("\n")

	out.printf("==== DAILY STATISTICS ====================\n")
	for _, day := range daily {
		s := day.Stats
		out.printf("%s - %s recorded activity (%.1f%%)\n",
			day.Date.Format(dateBracketed),
			FormatDuration(s.TotalDuration),
			s.TotalDuration.Seconds()*100.0/86400.0)
		out.printf("      Total active power: %.2fkWh  | Average: %.2fkW  | Maximum: %.2fkW on %s\n",
			s.TotalActivePower, s.AvgActivePower, s.MaxActivePower.Power,
			s.MaxActivePower.Timestamp.Format(tsBracketed))
		out.printf("    Total apparent power: %.2fkVAh | Average: %.2fkVA | Maximum: %.2fkVA on %s\n",
			s.TotalApparentPower, s.AvgApparentPower, s.MaxApparentPower.ApparentPower,
			s.MaxApparentPower.Timestamp.Format(tsBracketed))
		out.printf("    Voltage: Average: %.1fV | Minimum: %.1fV on %s | Maximum: %.1fV on %s\n",
			s.AvgVoltage,
			s.MinVoltage.Voltage, s.MinVoltage.Timestamp.Format(tsBracketed),
			s.MaxVoltage.Voltage, s.MaxVoltage.Timestamp.Format(tsBracketed))
		out.printf("\n")
	}

	out.printf("\n")
	out.printf("==== BLACKOUT HISTORY ====================\n")
	out.printf("%d blackout(s) for a total of %s.\n",
		blackouts.Count, FormatDuration(blackouts.TotalDuration))
	out.printf("\n")
	for _, b := range blackouts.Blackouts {
		out.printf("%s Duration: %s\n", b.Timestamp.Format(tsBracketed), FormatDuration(b.Duration))
	}
	return out.err
}

// FormatDuration renders a duration as dd:hh:mm, dropping the leading units
// when they are zero.
func FormatDuration(d time.Duration) string {
	seconds := int64(d / time.Second)
	minutes := (seconds / 60) % 60
	hours := (seconds / 3600) % 24
	days := seconds / 86400

	switch {
	case days > 0:
		return fmt.Sprintf("%02dd:%02dh:%02dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%02dh:%02dm", hours, minutes)
	default:
		return fmt.Sprintf("%02dm", minutes)
	}
}

// formatFloat renders a value as the shortest decimal that reads back exactly,
// never in exponent notation.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// writeFile hands a buffered writer to fn and puts the result at path
// atomically: it writes a temporary file and renames it over the target, so a
// failure partway through leaves the previous file untouched.
//
// os.Create truncated in place instead, which meant a failed run left a
// truncated file where a valid one had been. That corruption is silent, since a
// short CSV still opens cleanly in a spreadsheet; and the tool writes a few
// hundred kilobytes, sometimes onto the very card it just read, so a full disk
// is a realistic way to get there.
//
// Deliberately no fsync: the failure being prevented is clobbering a good file,
// not losing a new one to a machine crash.
func writeFile(path string, fn func(io.Writer) error) error {
	// The temp file has to share a filesystem with the target for os.Rename, so
	// it goes in the target's own directory. The dot prefix keeps a leftover
	// from a killed process out of the input list on the next run, for when
	// that directory is also the input folder.
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp)
		}
	}()

	// os.CreateTemp creates the file 0600, where os.Create gave 0666 & ^umask.
	// Without this the output would silently become owner-only. Overwriting
	// keeps whatever mode the target already had, so permissions a user
	// tightened by hand survive the next run.
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	bw := bufio.NewWriter(f)
	if err := fn(bw); err != nil {
		_ = f.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	// Unlike the paths above, a Close error here is the only report that the
	// data did not land, so it is returned rather than discarded.
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	renamed = true
	return nil
}

// errWriter collects the first write error so that long runs of formatted
// output do not need to check every call.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, a...)
}
