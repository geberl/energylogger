package voltcraft

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// testBlock is the fixture used by the original Rust implementation's unit
// tests: one block header, one sample, end of data.
var testBlock = []byte{
	0xE0, 0xC5, 0xEA, // magic number
	0x09, 0x0B, 0x0E, 0x12, 0x2B, // 2014-09-11 18:43
	0x08, 0xC6, 0x01, 0xBE, 0x57, // 224.6 V, 0.446 A, cos(phi) 0.87
	0xFF, 0xFF, 0xFF, 0xFF, // end of data
}

// record builds a 5-byte sample from raw device units: decivolts, milliamperes
// and cos(phi) in percent.
func record(deciVolts, milliAmps uint16, powerFactorPercent byte) []byte {
	return []byte{
		byte(deciVolts >> 8), byte(deciVolts),
		byte(milliAmps >> 8), byte(milliAmps),
		powerFactorPercent,
	}
}

// block builds a block header followed by the given samples.
func block(month, day, year, hour, minute byte, records ...[]byte) []byte {
	out := []byte{0xE0, 0xC5, 0xEA, month, day, year, hour, minute}
	for _, r := range records {
		out = append(out, r...)
	}
	return out
}

var endMarker = []byte{0xFF, 0xFF, 0xFF, 0xFF}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestParseBytesDecodesSample(t *testing.T) {
	events, err := ParseBytes(testBlock)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	got := events[0]

	// Unlike the Rust test this asserts the recorded wall-clock digits in their
	// fixed storage location, so it does not depend on the machine's timezone.
	want := time.Date(2014, time.September, 11, 18, 43, 0, 0, time.UTC)
	if !got.Timestamp.Equal(want) {
		t.Errorf("timestamp = %s, want %s", got.Timestamp, want)
	}
	if got.Voltage != 224.6 {
		t.Errorf("voltage = %v, want 224.6", got.Voltage)
	}
	if got.Current != 0.446 {
		t.Errorf("current = %v, want 0.446", got.Current)
	}
	if got.PowerFactor != 0.87 {
		t.Errorf("power factor = %v, want 0.87", got.PowerFactor)
	}
	if wantPower := 224.6 * 0.446 * 0.87 / 1000.0; !almostEqual(got.Power, wantPower) {
		t.Errorf("power = %v, want %v", got.Power, wantPower)
	}
	if wantApparent := 224.6 * 0.446 / 1000.0; !almostEqual(got.ApparentPower, wantApparent) {
		t.Errorf("apparent power = %v, want %v", got.ApparentPower, wantApparent)
	}
}

func TestParseBytesIncrementsTimestampPerSample(t *testing.T) {
	raw := block(9, 11, 14, 18, 43,
		record(2246, 446, 87),
		record(2247, 446, 86),
		record(2248, 444, 87),
	)
	raw = append(raw, endMarker...)

	events, err := ParseBytes(raw)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, e := range events {
		want := time.Date(2014, time.September, 11, 18, 43+i, 0, 0, time.UTC)
		if !e.Timestamp.Equal(want) {
			t.Errorf("event %d timestamp = %s, want %s", i, e.Timestamp, want)
		}
	}
}

func TestParseBytesHandlesMultipleBlocks(t *testing.T) {
	// A second block mid-file re-anchors the timestamp and resets the
	// per-sample minute counter.
	raw := block(9, 11, 14, 18, 43, record(2246, 446, 87), record(2247, 446, 87))
	raw = append(raw, block(12, 31, 15, 23, 58, record(2200, 100, 50))...)
	raw = append(raw, endMarker...)

	events, err := ParseBytes(raw)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	want := []time.Time{
		time.Date(2014, time.September, 11, 18, 43, 0, 0, time.UTC),
		time.Date(2014, time.September, 11, 18, 44, 0, 0, time.UTC),
		time.Date(2015, time.December, 31, 23, 58, 0, 0, time.UTC),
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i, ts := range want {
		if !events[i].Timestamp.Equal(ts) {
			t.Errorf("event %d timestamp = %s, want %s", i, events[i].Timestamp, ts)
		}
	}
}

func TestParseBytesRejectsForeignFiles(t *testing.T) {
	tests := map[string][]byte{
		"empty":                         {},
		"shorter than the magic number": {0xE0, 0xC5},
		"INFO metadata file":            []byte("INFO:\x02 \xdd"),
		"padding only":                  {0xFF, 0xFF, 0xFF, 0xFF},
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBytes(raw); !errors.Is(err, ErrNotVoltcraftFile) {
				t.Errorf("err = %v, want ErrNotVoltcraftFile", err)
			}
		})
	}
}

func TestParseBytesRejectsTruncatedData(t *testing.T) {
	tests := map[string][]byte{
		"header only":        block(9, 11, 14, 18, 43),
		"partial header":     {0xE0, 0xC5, 0xEA, 0x09, 0x0B},
		"partial sample":     append(block(9, 11, 14, 18, 43), 0x08, 0xC6, 0x01),
		"no end-of-data":     block(9, 11, 14, 18, 43, record(2246, 446, 87)),
		"partial end marker": append(block(9, 11, 14, 18, 43, record(2246, 446, 87)), 0xFF, 0xFF),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBytes(raw); !errors.Is(err, ErrTruncated) {
				t.Errorf("err = %v, want ErrTruncated", err)
			}
		})
	}
}

func TestParseBytesRejectsImplausibleVoltage(t *testing.T) {
	// The bounds are exclusive, so exactly 150.0 V and exactly 250.0 V are
	// rejected too.
	for _, deciVolts := range []uint16{0, 1499, 1500, 2500, 2501, 65535} {
		raw := append(block(9, 11, 14, 18, 43, record(deciVolts, 446, 87)), endMarker...)
		if _, err := ParseBytes(raw); !errors.Is(err, ErrVoltageRange) {
			t.Errorf("%.1fV: err = %v, want ErrVoltageRange", float64(deciVolts)/10, err)
		}
	}
	// Just inside the bounds still parses.
	for _, deciVolts := range []uint16{1501, 2499} {
		raw := append(block(9, 11, 14, 18, 43, record(deciVolts, 446, 87)), endMarker...)
		if _, err := ParseBytes(raw); err != nil {
			t.Errorf("%.1fV: unexpected error %v", float64(deciVolts)/10, err)
		}
	}
}

func TestParseBytesRejectsImpossibleTimestamps(t *testing.T) {
	tests := map[string][]byte{
		"month 0":     block(0, 11, 14, 18, 43, record(2246, 446, 87)),
		"month 13":    block(13, 11, 14, 18, 43, record(2246, 446, 87)),
		"day 0":       block(9, 0, 14, 18, 43, record(2246, 446, 87)),
		"day 32":      block(9, 32, 14, 18, 43, record(2246, 446, 87)),
		"30 February": block(2, 30, 14, 18, 43, record(2246, 446, 87)),
		"hour 24":     block(9, 11, 14, 24, 43, record(2246, 446, 87)),
		"minute 60":   block(9, 11, 14, 18, 60, record(2246, 446, 87)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			raw = append(raw, endMarker...)
			if _, err := ParseBytes(raw); !errors.Is(err, ErrInvalidTimestamp) {
				t.Errorf("err = %v, want ErrInvalidTimestamp", err)
			}
		})
	}
}

func TestParseBytesIgnoresPaddingAfterEndOfData(t *testing.T) {
	// Real files are 10244 bytes, padded with 0xFF after the end marker.
	raw := append(block(9, 11, 14, 18, 43, record(2246, 446, 87)), endMarker...)
	for len(raw) < 10244 {
		raw = append(raw, 0xFF)
	}
	events, err := ParseBytes(raw)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
}

func TestParseFileRealCapture(t *testing.T) {
	// A real 10244-byte capture straight off the device's SD card.
	events, err := ParseFile(filepath.Join("..", "..", "testdata", "A04FC8D2.BIN"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events decoded")
	}
	first := time.Date(2014, time.September, 11, 18, 43, 0, 0, time.UTC)
	if !events[0].Timestamp.Equal(first) {
		t.Errorf("first timestamp = %s, want %s", events[0].Timestamp, first)
	}
	if events[0].Voltage != 224.6 || events[0].Current != 0.446 || events[0].PowerFactor != 0.87 {
		t.Errorf("first sample = %.1fV %.3fA %.2f, want 224.6V 0.446A 0.87",
			events[0].Voltage, events[0].Current, events[0].PowerFactor)
	}
	// Every sample must carry a plausible mains voltage and a cos(phi) at most 1.
	for i, e := range events {
		if e.Voltage <= minVoltage || e.Voltage >= maxVoltage {
			t.Fatalf("event %d: implausible voltage %v", i, e.Voltage)
		}
		if e.PowerFactor < 0 || e.PowerFactor > 1 {
			t.Fatalf("event %d: power factor %v out of range", i, e.PowerFactor)
		}
	}
}

func TestParseFileRejectsDeviceMetadataFile(t *testing.T) {
	// The device also writes a small "INFO:" file, which carries no samples.
	_, err := ParseFile(filepath.Join("..", "..", "testdata", "INFO_A060AB85.BIN"))
	if !errors.Is(err, ErrNotVoltcraftFile) {
		t.Errorf("err = %v, want ErrNotVoltcraftFile", err)
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := ParseFile(filepath.Join(t.TempDir(), "nope.BIN")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
