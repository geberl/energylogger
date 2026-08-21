package voltcraft

import (
	"testing"
	"time"
)

var base = time.Date(2014, time.July, 20, 22, 0, 0, 0, time.UTC)

// event builds a sample at base + minute, with the given electrical values.
func event(minute int, voltage, current, powerFactor float64) PowerEvent {
	return PowerEvent{
		Timestamp:     base.Add(time.Duration(minute) * time.Minute),
		Voltage:       voltage,
		Current:       current,
		PowerFactor:   powerFactor,
		Power:         voltage * current * powerFactor / 1000.0,
		ApparentPower: voltage * current / 1000.0,
	}
}

func TestComputeStatsAggregates(t *testing.T) {
	// Two samples of exactly 1 kW and 2 kW of apparent power, at cos(phi) 0.5.
	events := []PowerEvent{
		{Timestamp: base, Voltage: 200, Current: 5, PowerFactor: 0.5, Power: 0.5, ApparentPower: 1.0},
		{Timestamp: base.Add(time.Minute), Voltage: 220, Current: 10, PowerFactor: 0.5, Power: 1.0, ApparentPower: 2.0},
	}
	got := computeStats(events)

	// Each sample stands for a minute, so a sum of powers is an energy in
	// kWh once divided by 60.
	if want := 1.5 / 60.0; !almostEqual(got.TotalActivePower, want) {
		t.Errorf("TotalActivePower = %v, want %v", got.TotalActivePower, want)
	}
	if want := 0.75; !almostEqual(got.AvgActivePower, want) {
		t.Errorf("AvgActivePower = %v, want %v", got.AvgActivePower, want)
	}
	if want := 3.0 / 60.0; !almostEqual(got.TotalApparentPower, want) {
		t.Errorf("TotalApparentPower = %v, want %v", got.TotalApparentPower, want)
	}
	if want := 1.5; !almostEqual(got.AvgApparentPower, want) {
		t.Errorf("AvgApparentPower = %v, want %v", got.AvgApparentPower, want)
	}
	if want := 210.0; !almostEqual(got.AvgVoltage, want) {
		t.Errorf("AvgVoltage = %v, want %v", got.AvgVoltage, want)
	}
	if got.MaxActivePower.Power != 1.0 {
		t.Errorf("MaxActivePower = %v, want 1.0", got.MaxActivePower.Power)
	}
	if got.MaxApparentPower.ApparentPower != 2.0 {
		t.Errorf("MaxApparentPower = %v, want 2.0", got.MaxApparentPower.ApparentPower)
	}
	if got.MinVoltage.Voltage != 200 || got.MaxVoltage.Voltage != 220 {
		t.Errorf("voltage range = %v..%v, want 200..220", got.MinVoltage.Voltage, got.MaxVoltage.Voltage)
	}
	// Two samples one minute apart cover two minutes of recording.
	if want := 2 * time.Minute; got.TotalDuration != want {
		t.Errorf("TotalDuration = %s, want %s", got.TotalDuration, want)
	}
}

func TestComputeStatsTieBreaking(t *testing.T) {
	// The original implementation reports the last of several equal maxima and
	// the first of several equal minima; matching that keeps the reported
	// timestamps identical.
	events := []PowerEvent{
		event(0, 210, 1, 1),
		event(1, 210, 1, 1),
		event(2, 210, 1, 1),
	}
	got := computeStats(events)

	if want := base.Add(2 * time.Minute); !got.MaxActivePower.Timestamp.Equal(want) {
		t.Errorf("MaxActivePower timestamp = %s, want the last tied sample %s", got.MaxActivePower.Timestamp, want)
	}
	if want := base.Add(2 * time.Minute); !got.MaxApparentPower.Timestamp.Equal(want) {
		t.Errorf("MaxApparentPower timestamp = %s, want the last tied sample %s", got.MaxApparentPower.Timestamp, want)
	}
	if want := base.Add(2 * time.Minute); !got.MaxVoltage.Timestamp.Equal(want) {
		t.Errorf("MaxVoltage timestamp = %s, want the last tied sample %s", got.MaxVoltage.Timestamp, want)
	}
	if !got.MinVoltage.Timestamp.Equal(base) {
		t.Errorf("MinVoltage timestamp = %s, want the first tied sample %s", got.MinVoltage.Timestamp, base)
	}
}

func TestComputeStatsEmpty(t *testing.T) {
	if got := computeStats(nil); got != (PowerStats{}) {
		t.Errorf("computeStats(nil) = %+v, want the zero value", got)
	}
}

func TestOverallOmitsAverageBelowOneDay(t *testing.T) {
	events := []PowerEvent{event(0, 210, 1, 1), event(60*24-1, 210, 1, 1)}
	if got := NewStatistics(events).Overall(); got.AvgDailyConsumption != nil {
		t.Errorf("AvgDailyConsumption = %v, want nil for less than a day of data", *got.AvgDailyConsumption)
	}
}

func TestOverallAverageDailyConsumption(t *testing.T) {
	// Exactly two days apart, so the daily average is half the total energy.
	events := []PowerEvent{event(0, 210, 1, 1), event(2*24*60, 210, 1, 1)}
	got := NewStatistics(events).Overall()
	if got.AvgDailyConsumption == nil {
		t.Fatal("AvgDailyConsumption = nil, want a value")
	}
	if want := got.Stats.TotalActivePower / 2.0; !almostEqual(*got.AvgDailyConsumption, want) {
		t.Errorf("AvgDailyConsumption = %v, want %v", *got.AvgDailyConsumption, want)
	}
	if !got.Start.Equal(base) {
		t.Errorf("Start = %s, want %s", got.Start, base)
	}
	if want := base.Add(2 * 24 * time.Hour); !got.End.Equal(want) {
		t.Errorf("End = %s, want %s", got.End, want)
	}
}

func TestOverallEmpty(t *testing.T) {
	got := NewStatistics(nil).Overall()
	if !got.Start.IsZero() || !got.End.IsZero() || got.AvgDailyConsumption != nil {
		t.Errorf("Overall() = %+v, want the zero value", got)
	}
}

func TestDailyGroupsByCalendarDay(t *testing.T) {
	// base is 22:00, so a sample two hours later falls on the next day.
	events := []PowerEvent{
		event(0, 210, 1, 1),
		event(60, 210, 1, 1),
		event(120, 210, 1, 1), // 00:00 the next day
		event(180, 210, 1, 1),
		event(3*24*60, 210, 1, 1), // three days later
	}
	daily := NewStatistics(events).Daily()
	if len(daily) != 3 {
		t.Fatalf("got %d days, want 3", len(daily))
	}
	wantDates := []time.Time{
		time.Date(2014, time.July, 20, 0, 0, 0, 0, time.UTC),
		time.Date(2014, time.July, 21, 0, 0, 0, 0, time.UTC),
		time.Date(2014, time.July, 23, 0, 0, 0, 0, time.UTC),
	}
	for i, want := range wantDates {
		if !daily[i].Date.Equal(want) {
			t.Errorf("day %d = %s, want %s", i, daily[i].Date, want)
		}
	}
	// The first day holds two samples an hour apart: 61 minutes of activity.
	if want := 61 * time.Minute; daily[0].Stats.TotalDuration != want {
		t.Errorf("day 0 duration = %s, want %s", daily[0].Stats.TotalDuration, want)
	}
	if want := time.Minute; daily[2].Stats.TotalDuration != want {
		t.Errorf("day 2 duration = %s, want %s", daily[2].Stats.TotalDuration, want)
	}
}

func TestDailyEmpty(t *testing.T) {
	if daily := NewStatistics(nil).Daily(); len(daily) != 0 {
		t.Errorf("got %d days, want none", len(daily))
	}
}

func TestBlackoutsDetectsGaps(t *testing.T) {
	events := []PowerEvent{
		event(0, 210, 1, 1),
		event(1, 210, 1, 1), // no gap
		event(4, 210, 1, 1), // two minutes missing
		event(5, 210, 1, 1),
		event(65, 210, 1, 1), // an hour missing, less the sampled minute
	}
	got := NewStatistics(events).Blackouts()

	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2", got.Count)
	}
	if want := 61 * time.Minute; got.TotalDuration != want {
		t.Errorf("TotalDuration = %s, want %s", got.TotalDuration, want)
	}
	// A blackout starts at the first minute with no sample.
	if want := base.Add(2 * time.Minute); !got.Blackouts[0].Timestamp.Equal(want) {
		t.Errorf("blackout 0 start = %s, want %s", got.Blackouts[0].Timestamp, want)
	}
	if want := 2 * time.Minute; got.Blackouts[0].Duration != want {
		t.Errorf("blackout 0 duration = %s, want %s", got.Blackouts[0].Duration, want)
	}
	if want := base.Add(6 * time.Minute); !got.Blackouts[1].Timestamp.Equal(want) {
		t.Errorf("blackout 1 start = %s, want %s", got.Blackouts[1].Timestamp, want)
	}
	if want := 59 * time.Minute; got.Blackouts[1].Duration != want {
		t.Errorf("blackout 1 duration = %s, want %s", got.Blackouts[1].Duration, want)
	}
}

func TestBlackoutsNoneWhenContiguous(t *testing.T) {
	var events []PowerEvent
	for i := range 10 {
		events = append(events, event(i, 210, 1, 1))
	}
	if got := NewStatistics(events).Blackouts(); got.Count != 0 || got.TotalDuration != 0 {
		t.Errorf("got %d blackouts totalling %s, want none", got.Count, got.TotalDuration)
	}
}

func TestBlackoutsSingleEvent(t *testing.T) {
	if got := NewStatistics([]PowerEvent{event(0, 210, 1, 1)}).Blackouts(); got.Count != 0 {
		t.Errorf("Count = %d, want 0", got.Count)
	}
}
