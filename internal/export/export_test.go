package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"energylogger/internal/voltcraft"
)

var base = time.Date(2014, time.July, 20, 22, 4, 0, 0, time.UTC)

// sampleEvent derives power the way the parser does. The arithmetic has to
// happen at run time: Go folds constant expressions at arbitrary precision and
// rounds once, which yields different last digits than rounding at every step.
func sampleEvent(minute int, voltage, current, powerFactor float64) voltcraft.PowerEvent {
	return voltcraft.PowerEvent{
		Timestamp:     base.Add(time.Duration(minute) * time.Minute),
		Voltage:       voltage,
		Current:       current,
		PowerFactor:   powerFactor,
		Power:         voltage * current * powerFactor / 1000.0,
		ApparentPower: voltage * current / 1000.0,
	}
}

func sampleEvents() []voltcraft.PowerEvent {
	return []voltcraft.PowerEvent{
		sampleEvent(0, 211.3, 0.067, 0.83),
		sampleEvent(1, 209.0, 0.040, 0.82),
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "00m"},
		{30 * time.Second, "00m"},
		{time.Minute, "01m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "01h:00m"},
		{time.Hour + 56*time.Minute, "01h:56m"},
		{23*time.Hour + 59*time.Minute, "23h:59m"},
		{24 * time.Hour, "01d:00h:00m"},
		{54*24*time.Hour + time.Hour + 55*time.Minute, "54d:01h:55m"},
		{10*time.Hour + 50*time.Minute, "10h:50m"},
		{100*24*time.Hour + 5*time.Hour, "100d:05h:00m"},
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.in); got != tt.want {
			t.Errorf("FormatDuration(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWriteHistoryText(t *testing.T) {
	var sb strings.Builder
	if err := WriteHistoryText(&sb, sampleEvents()); err != nil {
		t.Fatalf("WriteHistoryText: %v", err)
	}
	want := "== PARAMETER HISTORY ==\n" +
		"\n" +
		"[2014-07-20 22:04] U=211.3V I=0.067A cosPHI=0.83 P=0.012kW S=0.014kVA\n" +
		"[2014-07-20 22:05] U=209.0V I=0.040A cosPHI=0.82 P=0.007kW S=0.008kVA\n"
	if got := sb.String(); got != want {
		t.Errorf("history text mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestWriteHistoryCSV(t *testing.T) {
	var sb strings.Builder
	if err := WriteHistoryCSV(&sb, sampleEvents()); err != nil {
		t.Fatalf("WriteHistoryCSV: %v", err)
	}
	got := sb.String()

	// Line endings are plain LF, as in the original tool.
	if strings.Contains(got, "\r") {
		t.Error("expected LF line endings, found a carriage return")
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	wantHeader := "Timestamp (device local time),Voltage (V),Current (A),cosPHI,Active Power (kW),Apparent Power (kVA)"
	if lines[0] != wantHeader {
		t.Errorf("header = %q, want %q", lines[0], wantHeader)
	}
	// Values are unrounded, so the full precision of the computation shows.
	wantFirst := "2014-07-20 22:04,211.3,0.067,0.83,0.011750393000000001,0.014157100000000002"
	if lines[1] != wantFirst {
		t.Errorf("first record = %q, want %q", lines[1], wantFirst)
	}
}

func TestFormatFloatAvoidsExponentNotation(t *testing.T) {
	tests := map[float64]string{
		0:            "0",
		224.6:        "224.6",
		0.446:        "0.446",
		0.0000001234: "0.0000001234",
		1e21:         "1000000000000000000000",
	}
	for in, want := range tests {
		if got := formatFloat(in); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

// stats builds an overall/daily/blackout set where active and apparent power
// differ in every field, so that a line printing the wrong one is visible.
func stats() (voltcraft.OverallInfo, []voltcraft.DailyInfo, voltcraft.BlackoutInfo) {
	activePeak := voltcraft.PowerEvent{
		Timestamp: base, Voltage: 230, Current: 4, PowerFactor: 0.5,
		Power: 0.46, ApparentPower: 0.92,
	}
	apparentPeak := voltcraft.PowerEvent{
		Timestamp: base.Add(time.Minute), Voltage: 230, Current: 8, PowerFactor: 0.25,
		Power: 0.46, ApparentPower: 1.84,
	}
	avgDaily := 12.5
	powerStats := voltcraft.PowerStats{
		TotalActivePower:   1.5,
		AvgActivePower:     0.25,
		MaxActivePower:     activePeak,
		TotalApparentPower: 3.5,
		AvgApparentPower:   0.75,
		MaxApparentPower:   apparentPeak,
		MinVoltage:         activePeak,
		MaxVoltage:         apparentPeak,
		AvgVoltage:         230,
		TotalDuration:      2 * time.Minute,
	}
	overall := voltcraft.OverallInfo{
		Start:               base,
		End:                 base.Add(48 * time.Hour),
		Stats:               powerStats,
		AvgDailyConsumption: &avgDaily,
	}
	daily := []voltcraft.DailyInfo{{
		Date:  time.Date(2014, time.July, 20, 0, 0, 0, 0, time.UTC),
		Stats: powerStats,
	}}
	blackouts := voltcraft.BlackoutInfo{
		Count:         1,
		TotalDuration: 5 * time.Minute,
		Blackouts: []voltcraft.Blackout{
			{Timestamp: base.Add(2 * time.Minute), Duration: 5 * time.Minute},
		},
	}
	return overall, daily, blackouts
}

func TestWriteStatistics(t *testing.T) {
	overall, daily, blackouts := stats()

	var sb strings.Builder
	if err := WriteStatistics(&sb, overall, daily, blackouts); err != nil {
		t.Fatalf("WriteStatistics: %v", err)
	}
	want := `==== OVERALL STATISTICS ==================
Interval: [2014-07-20 22:04]-[2014-07-22 22:04] (02d:00h:00m)
Average consumption: 12.50kWh/day | Projected: 375.00kWh/month or 4562.50kWh/year.

- ACTIVE POWER
Total energy consumption: 1.50kWh.
Peak power was 0.46kW and occured on [2014-07-20 22:04].
Minute by minute average power: 0.25kW.

- APPARENT POWER
Total energy consumption: 3.50kVAh.
Peak power was 1.84kVA and occured on [2014-07-20 22:05].
Minute by minute average power: 0.75kVA.

- VOLTAGE
Minimum voltage was 230.0V and occured on [2014-07-20 22:04].
Maximum voltage was 230.0V and occured on [2014-07-20 22:05].
Minute by minute average voltage: 230.0V.


==== DAILY STATISTICS ====================
[2014-07-20] - 02m recorded activity (0.1%)
      Total active power: 1.50kWh  | Average: 0.25kW  | Maximum: 0.46kW on [2014-07-20 22:04]
    Total apparent power: 3.50kVAh | Average: 0.75kVA | Maximum: 1.84kVA on [2014-07-20 22:05]
    Voltage: Average: 230.0V | Minimum: 230.0V on [2014-07-20 22:04] | Maximum: 230.0V on [2014-07-20 22:05]


==== BLACKOUT HISTORY ====================
1 blackout(s) for a total of 05m.

[2014-07-20 22:06] Duration: 05m
`
	if got := sb.String(); got != want {
		t.Errorf("statistics mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestWriteStatisticsOmitsAverageWhenUnset(t *testing.T) {
	overall, daily, blackouts := stats()
	overall.AvgDailyConsumption = nil

	var sb strings.Builder
	if err := WriteStatistics(&sb, overall, daily, blackouts); err != nil {
		t.Fatalf("WriteStatistics: %v", err)
	}
	if strings.Contains(sb.String(), "Average consumption") {
		t.Error("average consumption line should be omitted when unset")
	}
}

func TestWriteFileHelpers(t *testing.T) {
	dir := t.TempDir()
	events := sampleEvents()
	overall, daily, blackouts := stats()

	paths := map[string]func() error{
		"history.txt": func() error { return WriteHistoryTextFile(filepath.Join(dir, "history.txt"), events) },
		"history.csv": func() error { return WriteHistoryCSVFile(filepath.Join(dir, "history.csv"), events) },
		"stats.txt": func() error {
			return WriteStatisticsFile(filepath.Join(dir, "stats.txt"), overall, daily, blackouts)
		},
	}
	for name, write := range paths {
		if err := write(); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if len(content) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestWriteFileFailsOnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "history.txt")
	if err := WriteHistoryTextFile(path, sampleEvents()); err == nil {
		t.Error("expected an error writing into a nonexistent directory")
	}
}
