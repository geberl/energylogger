# ![EnergyLogger-logo](assets/logo-small.png) energylogger

Command-line analyzer for the **Voltcraft Energy-Logger 4000**. It decodes the
`.BIN` files the device writes to its SD card and produces a minute-by-minute
parameter history plus daily, overall and blackout statistics.

Go port of [vbocan/voltcraft-energy-analyzer](https://github.com/vbocan/voltcraft-energy-analyzer)
by Valer Bocan. The only dependency is
[peterbourgon/ff](https://github.com/peterbourgon/ff) for flag and environment
parsing.

## Build

```bash
go build ./cmd/energylogger
```

## Usage

```
energylogger [flags]
```

Both folders default to the current directory.

| Flag | Environment variable | Meaning |
|---|---|---|
| `-input <dir>` | `ENERGYLOGGER_INPUT` | directory to read Voltcraft `.BIN` files from |
| `-output <dir>` | `ENERGYLOGGER_OUTPUT` | directory to write the output files to (created if missing) |
| `-quiet` | `ENERGYLOGGER_QUIET` | suppress the banner and per-file progress output |
| `-no-color` | `ENERGYLOGGER_NO_COLOR` | disable ANSI colour (also honours `NO_COLOR` and a non-terminal stdout) |
| `-h`, `--help`, `/?` | — | show help |

Every setting can come from either source, in this order of precedence: a flag on
the command line, then the environment variable, then the default.

```bash
energylogger -input /Volumes/SDCARD -output ~/energy   # read the SD card, write to ~/energy
energylogger -input /Volumes/SDCARD                    # write to the current directory
energylogger                                           # read and write in the current directory

export ENERGYLOGGER_INPUT=/Volumes/SDCARD              # or keep the folders in the
export ENERGYLOGGER_OUTPUT=~/energy                    # environment, e.g. in .envrc
energylogger
```

Every file in the input folder is tried; anything that is not a Voltcraft data
file is reported as `Invalid` and skipped, including the small `INFO:` metadata
file the device also writes. Subdirectories and dotfiles such as `.DS_Store` are
passed over silently. Samples from all files are merged, sorted and deduplicated
by timestamp, so dumping the same SD card twice is harmless.

Deduplication keeps the first sample of each timestamp and reports how many it
dropped. If any of the dropped samples carried *different* readings from the one
kept, that is not a re-dump and a real measurement was lost, so the tool warns
about it — see [Timestamps](#timestamps).

Exit code is 0 on success, 1 if the input folder cannot be read, the output
folder is unusable, or a file could not be written, and 2 for a bad flag or
environment variable. An input folder that does not exist is an error, not an
empty run, since it usually means a mistyped path or an unmounted card.

### Output files

| File | Contents |
|---|---|
| `voltcraft_history.txt` | one line per recorded minute: voltage, current, cos φ, active and apparent power |
| `voltcraft_history.csv` | the same history, values unrounded, for spreadsheets |
| `voltcraft_stats.txt` | overall statistics, per-day statistics, and the blackout history |

```
==== OVERALL STATISTICS ==================
Interval: [2014-07-20 22:04]-[2014-09-12 23:59] (54d:01h:55m)
Average consumption: 1.53kWh/day | Projected: 46.03kWh/month or 559.98kWh/year.

- ACTIVE POWER
Total energy consumption: 82.97kWh.
Peak power was 1.34kW and occurred on [2014-08-26 07:52].
Minute by minute average power: 0.06kW.
...
```

Energy totals only count minutes that were actually recorded, so a day with a
blackout reports the energy used while logging, not an extrapolation. The
per-day "recorded activity" percentage spans the first to the last sample of the
day and therefore includes any gaps in between.

Two duration quirks are inherited from the Rust original and kept for
comparability. The overall `Interval` length is the plain distance between the
first and last sample, so it omits the final sample's own minute; per-day
durations add that minute, which is why a fully recorded day reads
`01d:00h:00m (100.0%)` while a month-long interval reads `29d:22h:05m`.
`Average consumption` likewise divides gap-inclusive elapsed time into energy
counted only over recorded minutes, so it under-reports on data with long
blackouts.

## Timestamps

The device clock is set by hand and stores no timezone, so a recording carries
nothing but the wall-clock digits the device displayed. Those digits are printed
back verbatim, in `YYYY-MM-DD HH:MM` at one-minute resolution — the finest the
format offers, since a block header has no seconds field.

Internally the timestamps are held in `time.UTC`. That is a storage choice, not
a claim that the readings are UTC: it keeps every duration, day boundary and
blackout length free of the host's `TZ` and of DST jumps. Nothing converts them,
and nothing should.

The practical consequence is about the *device's* clock, not the machine running
this tool. Leave the device clock alone and a DST changeover is invisible here.
Adjust it mid-recording and:

- **Spring forward** — the hour you skipped looks like a one-hour gap, so it is
  reported as a blackout that never happened.
- **Fall back** — the repeated hour produces duplicate timestamps. Dedup keeps
  the first sample of each minute and discards the rest, so up to 60 real
  samples and their energy drop out of the statistics. The tool warns when this
  happens, since the discarded samples carried different readings.

The same warning covers the other way duplicates arise: cards from two different
devices sitting in one input folder.

In the CSV the column is named `Timestamp (device local time)` rather than plain
`Timestamp`, so importers are less likely to reinterpret it in the reader's own
timezone.

## File format

The `.BIN` files hold a flat byte stream: no file header, no length field, no
checksum. Real data files are exactly 10244 bytes.

```
BLOCK:  E0 C5 EA        3-byte magic number
        MM DD YY HH MI  block start timestamp, plain binary (not BCD), year + 2000
        <record>...     5 bytes each, one sample per minute
EOD:    FF FF FF FF     end of data; the rest of the file is 0xFF padding

RECORD: voltage      uint16 big-endian / 10    -> V
        current      uint16 big-endian / 1000  -> A
        powerfactor  uint8            / 100    -> cos φ

derived: active power   = V × A × cos φ / 1000   (kW)
         apparent power = V × A / 1000           (kVA)
```

Records carry no timestamp of their own: the n-th record of a block is stamped
*block start + n minutes*. A file can hold several blocks at arbitrary offsets,
one per recording session, so the magic number is tested at every record
position and the minute counter restarts at each block. Gaps between blocks and
between files are what the blackout detection reconstructs.

The device also writes a short file beginning with the ASCII text `INFO:`. It
carries no samples and is skipped.

## Differences from the Rust original

`voltcraft_history.txt` is **byte-identical** to the Rust tool's output.
`voltcraft_history.csv` differs only in its header line (item 4 below), and in
`voltcraft_stats.txt` the only differences are the apparent-power lines listed
first.

1. **Fixed:** the daily *Total apparent power* line printed the active-power
   figures (`src/export.rs:181-190` in the original), which is why every day's
   apparent values equalled its active ones in the original README. This port
   prints the real apparent-power totals, averages and peaks.
2. **Fixed:** the overall *Peak power … kVA* line printed the active power of
   the peak-apparent-power sample (`src/export.rs:114`). This port prints its
   apparent power.
3. **Timestamps are timezone-naive** rather than bound to the host timezone, as
   described under [Timestamps](#timestamps). The original attaches each reading
   to the machine's zone, which on a DST fall-back day labels one hour twice:
   for the bundled `sample_data2` it prints the 60 minutes of
   `2014-10-26 02:00`–`02:59` under two different instants each, and its
   durations, daily grouping and blackout lengths vary with `TZ`.
4. The CSV timestamp column is named `Timestamp (device local time)` instead of
   `Timestamp`, so spreadsheets are less likely to reinterpret a bare naive
   timestamp in the reader's own zone. The values themselves are unchanged.
5. **Duplicate timestamps are reported.** Dedup behaves as before, but the count
   of dropped samples is printed, and dropped samples whose readings disagreed
   with the one kept raise a warning instead of vanishing silently.
6. **No panics on damaged input.** A truncated file, a file with no end-of-data
   marker, an impossible date, or an implausible mains voltage is reported as
   invalid and skipped; the original aborted the whole run.
7. The tool's own output files are skipped when the input and output folders are
   the same, so a second run in the same directory does not try to parse them.
8. Per-day statistics are computed in a single pass instead of rescanning every
   sample once per day.

## Tests

```bash
go test ./...
```

The suite covers the parser (including the original's own byte fixture, block
boundaries, truncation, padding and out-of-range values), the statistics
(aggregation, extremum tie-breaking, day grouping, blackout gaps), the output
formats, and the command line. `TestRunGolden` runs the whole pipeline over the
real device captures in `testdata/` and compares all three outputs against
`testdata/golden/`. After an intentional format change, regenerate them and
review the diff:

```bash
go test -run TestRunGolden ./cmd/energylogger -update
```

Extremum tie-breaking follows the original deliberately — the last of several
equal maxima and the first of several equal minima — so that reported peak
timestamps stay comparable with the Rust tool's.

## Licence

MIT. This is a derivative work of the Rust original, MIT licensed,
Copyright (c) 2022 Valer BOCAN, PhD, CSSLP. See [LICENSE](LICENSE).
