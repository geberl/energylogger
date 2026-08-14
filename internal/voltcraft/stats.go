package voltcraft

import "time"

// PowerStats holds the statistics computed over a set of power events, either
// the whole recorded interval or a single day.
type PowerStats struct {
	TotalActivePower float64    // kWh
	AvgActivePower   float64    // kW
	MaxActivePower   PowerEvent // sample with the highest active power

	TotalApparentPower float64    // kVAh
	AvgApparentPower   float64    // kVA
	MaxApparentPower   PowerEvent // sample with the highest apparent power

	MinVoltage PowerEvent // sample with the lowest voltage
	MaxVoltage PowerEvent // sample with the highest voltage
	AvgVoltage float64    // V

	// TotalDuration spans the first to the last sample, plus the minute the
	// last sample itself represents. Gaps in between are included.
	TotalDuration time.Duration
}

// A Blackout is a gap in the recording: the device was without power, so no
// samples exist for that stretch.
type Blackout struct {
	Timestamp time.Time     // first minute without a sample
	Duration  time.Duration // length of the gap
}

// DailyInfo holds one calendar day's statistics.
type DailyInfo struct {
	Date  time.Time // midnight UTC of the day
	Stats PowerStats
}

// OverallInfo holds the statistics for the entire recorded interval.
type OverallInfo struct {
	Start time.Time
	End   time.Time
	Stats PowerStats

	// AvgDailyConsumption is the average consumption in kWh/day, or nil when
	// less than a day was recorded and the figure would be meaningless.
	AvgDailyConsumption *float64
}

// BlackoutInfo holds every detected gap in the recording.
type BlackoutInfo struct {
	Count         int
	TotalDuration time.Duration
	Blackouts     []Blackout
}

// Statistics computes statistics over a set of power events. The events must be
// sorted chronologically and free of duplicate timestamps; blackout detection
// and daily grouping both rely on it.
type Statistics struct {
	events []PowerEvent
}

// NewStatistics returns a Statistics over the given sorted, deduplicated events.
func NewStatistics(events []PowerEvent) *Statistics {
	return &Statistics{events: events}
}

// Overall computes the statistics for the entire recorded interval. It returns
// the zero value if there are no events.
func (s *Statistics) Overall() OverallInfo {
	if len(s.events) == 0 {
		return OverallInfo{}
	}
	stats := computeStats(s.events)
	start := s.events[0].Timestamp
	end := s.events[len(s.events)-1].Timestamp

	info := OverallInfo{Start: start, End: end, Stats: stats}
	// Averaging over less than a day says nothing useful, so leave it unset.
	if span := end.Sub(start); span >= 24*time.Hour {
		avg := stats.TotalActivePower / (span.Seconds() / 86400.0)
		info.AvgDailyConsumption = &avg
	}
	return info
}

// Daily computes per-day statistics, one entry per calendar day that has at
// least one sample, in chronological order.
func (s *Statistics) Daily() []DailyInfo {
	var daily []DailyInfo
	// The events are sorted, so each day is a contiguous run and one pass is
	// enough to split them.
	for start := 0; start < len(s.events); {
		day := startOfDay(s.events[start].Timestamp)
		end := start + 1
		for end < len(s.events) && startOfDay(s.events[end].Timestamp).Equal(day) {
			end++
		}
		daily = append(daily, DailyInfo{
			Date:  day,
			Stats: computeStats(s.events[start:end]),
		})
		start = end
	}
	return daily
}

// Blackouts detects every gap in the recording. A gap of more than one minute
// between consecutive samples means the device lost power.
func (s *Statistics) Blackouts() BlackoutInfo {
	var (
		blackouts []Blackout
		total     time.Duration
	)
	for i := 0; i+1 < len(s.events); i++ {
		gap := s.events[i+1].Timestamp.Sub(s.events[i].Timestamp)
		if gap <= time.Minute {
			continue
		}
		// The sample itself accounts for one minute; the rest is the outage.
		blackout := Blackout{
			Timestamp: s.events[i].Timestamp.Add(time.Minute),
			Duration:  gap - time.Minute,
		}
		blackouts = append(blackouts, blackout)
		total += blackout.Duration
	}
	return BlackoutInfo{
		Count:         len(blackouts),
		TotalDuration: total,
		Blackouts:     blackouts,
	}
}

// computeStats aggregates the given events. Every sample represents one minute,
// hence the division by 60 to turn a sum of instantaneous powers into energy.
//
// Extrema follow the tie-breaking of the original Rust implementation, so that
// the reported timestamps match it: the last of several equal maxima wins, and
// the first of several equal minima.
func computeStats(events []PowerEvent) PowerStats {
	if len(events) == 0 {
		return PowerStats{}
	}

	var powerSum, apparentSum, voltageSum float64
	maxActive, maxApparent, minVolt, maxVolt := events[0], events[0], events[0], events[0]
	first, last := events[0].Timestamp, events[0].Timestamp

	for _, e := range events {
		powerSum += e.Power
		apparentSum += e.ApparentPower
		voltageSum += e.Voltage

		if e.Power >= maxActive.Power {
			maxActive = e
		}
		if e.ApparentPower >= maxApparent.ApparentPower {
			maxApparent = e
		}
		if e.Voltage < minVolt.Voltage {
			minVolt = e
		}
		if e.Voltage >= maxVolt.Voltage {
			maxVolt = e
		}
		if e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if !e.Timestamp.Before(last) {
			last = e.Timestamp
		}
	}
	n := float64(len(events))

	return PowerStats{
		TotalActivePower:   powerSum / 60.0,
		AvgActivePower:     powerSum / n,
		MaxActivePower:     maxActive,
		TotalApparentPower: apparentSum / 60.0,
		AvgApparentPower:   apparentSum / n,
		MaxApparentPower:   maxApparent,
		MinVoltage:         minVolt,
		MaxVoltage:         maxVolt,
		AvgVoltage:         voltageSum / n,
		// The last sample stands for its own minute too.
		TotalDuration: last.Sub(first) + time.Minute,
	}
}

// startOfDay returns midnight of the day the timestamp falls in.
func startOfDay(ts time.Time) time.Time {
	return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, ts.Location())
}
