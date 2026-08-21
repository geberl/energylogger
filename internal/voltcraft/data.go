// Package voltcraft decodes the proprietary .BIN files written by the
// Voltcraft Energy-Logger 4000 to its SD card, and computes statistics over
// the decoded samples.
//
// File format (a flat byte stream; no file header, length field or checksum):
//
//	BLOCK:  E0 C5 EA        3-byte magic number
//	        MM DD YY HH MI  5-byte block start timestamp, plain binary, year + 2000
//	        <record>...     5 bytes each, one sample per minute
//	EOD:    FF FF FF FF     end of data; the rest of the file is 0xFF padding
//
//	RECORD: voltage      uint16 big-endian / 10    -> volts
//	        current      uint16 big-endian / 1000  -> amperes
//	        powerfactor  uint8            / 100    -> cos(phi)
//
// A file may contain several blocks at arbitrary offsets, one per recording
// session of the device, so the magic number is tested at every record
// position. Records carry no timestamp of their own: the n-th record of a
// block is timestamped block start + n minutes.
//
// # Timestamps
//
// The device clock is set by hand and carries no timezone, so a header holds
// nothing but the wall-clock digits the device displayed. Decoded timestamps
// are therefore civil time, not instants: they are held in time.UTC purely so
// that arithmetic and day grouping cannot depend on the host timezone, and are
// formatted back verbatim. Do not convert them to another location — that
// would shift the readings away from the clock the user actually saw.
package voltcraft

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
)

// Sizes and markers of the on-disk format.
var (
	magicNumber = []byte{0xE0, 0xC5, 0xEA}
	endOfData   = []byte{0xFF, 0xFF, 0xFF, 0xFF}
)

const (
	headerSize = 5 // month, day, year, hour, minute
	recordSize = 5 // voltage (2), current (2), power factor (1)
)

// Errors returned by ParseBytes and ParseFile. All are reported per file: the
// caller skips the file and carries on with the rest of the input.
var (
	// ErrNotVoltcraftFile means the magic number is missing at offset 0. The
	// device also writes a small metadata file starting with the ASCII text
	// "INFO:", which lands here.
	ErrNotVoltcraftFile = errors.New("invalid data file, probably not a Voltcraft file")

	// ErrTruncated means the data ran out before an end-of-data marker.
	ErrTruncated = errors.New("truncated data file")

	// ErrVoltageRange means a sample carried an implausible mains voltage,
	// which indicates the stream is misaligned rather than that the reading is
	// real.
	ErrVoltageRange = errors.New("voltage out of plausible range")

	// ErrInvalidTimestamp means a block header held a date that does not exist.
	ErrInvalidTimestamp = errors.New("invalid block timestamp")
)

// Plausible mains voltage bounds, exclusive on both ends, as in the original
// Rust implementation.
const (
	minVoltage = 150.0
	maxVoltage = 250.0
)

// A PowerEvent is one sample: the electrical parameters the device recorded
// during a single minute.
type PowerEvent struct {
	// Timestamp is the start of the sampled minute as the device's own clock
	// read it: civil time with no timezone, held in time.UTC so that durations
	// and day boundaries stay independent of the host timezone. See the package
	// documentation; it must not be converted to another location.
	Timestamp     time.Time
	Voltage       float64 // volts
	Current       float64 // amperes
	PowerFactor   float64 // cos(phi)
	Power         float64 // active power, kW
	ApparentPower float64 // apparent power, kVA
}

// ParseFile reads and decodes a single Voltcraft data file.
func ParseFile(path string) ([]PowerEvent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBytes(raw)
}

// ParseBytes decodes the contents of a Voltcraft data file. The returned events
// are in file order, which is chronological within each block but not
// necessarily across blocks or across files.
func ParseBytes(raw []byte) ([]PowerEvent, error) {
	// A valid file always opens with a block header.
	if !isDataBlock(raw, 0) {
		return nil, ErrNotVoltcraftFile
	}

	var (
		events    []PowerEvent
		blockTime time.Time
		minute    int
		offset    int
	)
	for {
		// A new block re-anchors the timestamp. Test this before the
		// end-of-data marker, so that a block is never mistaken for padding.
		if isDataBlock(raw, offset) {
			offset += len(magicNumber)
			ts, err := decodeTimestamp(raw, offset)
			if err != nil {
				return nil, err
			}
			blockTime, minute = ts, 0
			offset += headerSize
			continue
		}
		if isEndOfData(raw, offset) {
			break
		}
		event, err := decodeRecord(raw, offset)
		if err != nil {
			return nil, err
		}
		event.Timestamp = blockTime.Add(time.Duration(minute) * time.Minute)
		events = append(events, event)
		minute++
		offset += recordSize
	}
	return events, nil
}

// isDataBlock reports whether a block header starts at off.
func isDataBlock(raw []byte, off int) bool {
	if off < 0 || off+len(magicNumber) > len(raw) {
		return false
	}
	return string(raw[off:off+len(magicNumber)]) == string(magicNumber)
}

// isEndOfData reports whether the end-of-data marker starts at off.
func isEndOfData(raw []byte, off int) bool {
	if off < 0 || off+len(endOfData) > len(raw) {
		return false
	}
	return string(raw[off:off+len(endOfData)]) == string(endOfData)
}

// decodeTimestamp decodes a 5-byte block header timestamp. The fields are
// plain binary (not BCD) and in month/day/year order; the year is an offset
// from 2000 and seconds are always zero.
//
// time.UTC below is a storage location, not a claim about the reading: the
// header carries the device's local wall clock and no offset, so the digits are
// kept exactly as recorded. Using a fixed zone keeps every duration, day
// boundary and blackout length free of the host timezone and its DST jumps.
func decodeTimestamp(raw []byte, off int) (time.Time, error) {
	if off+headerSize > len(raw) {
		return time.Time{}, fmt.Errorf("%w: block header at offset %d", ErrTruncated, off)
	}
	var (
		month  = int(raw[off])
		day    = int(raw[off+1])
		year   = int(raw[off+2]) + 2000
		hour   = int(raw[off+3])
		minute = int(raw[off+4])
	)
	ts := time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.UTC)
	// time.Date normalises out-of-range fields (day 32 becomes the 1st of the
	// next month, and so on). Reject those instead, since they mean the stream
	// is misaligned.
	if ts.Year() != year || int(ts.Month()) != month || ts.Day() != day ||
		ts.Hour() != hour || ts.Minute() != minute {
		return time.Time{}, fmt.Errorf("%w at offset %d: %04d-%02d-%02d %02d:%02d",
			ErrInvalidTimestamp, off, year, month, day, hour, minute)
	}
	return ts, nil
}

// decodeRecord decodes one 5-byte sample. The returned event has no timestamp;
// the caller assigns it from the enclosing block.
func decodeRecord(raw []byte, off int) (PowerEvent, error) {
	if off+recordSize > len(raw) {
		return PowerEvent{}, fmt.Errorf("%w: record at offset %d", ErrTruncated, off)
	}
	voltage := float64(binary.BigEndian.Uint16(raw[off:off+2])) / 10.0
	if voltage <= minVoltage || voltage >= maxVoltage {
		return PowerEvent{}, fmt.Errorf("%w: %.1fV at offset %d", ErrVoltageRange, voltage, off)
	}
	current := float64(binary.BigEndian.Uint16(raw[off+2:off+4])) / 1000.0
	powerFactor := float64(raw[off+4]) / 100.0

	return PowerEvent{
		Voltage:       voltage,
		Current:       current,
		PowerFactor:   powerFactor,
		Power:         voltage * current * powerFactor / 1000.0, // kW
		ApparentPower: voltage * current / 1000.0,               // kVA
	}, nil
}
