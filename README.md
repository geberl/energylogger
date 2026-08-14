# energylogger

Command-line analyzer for the **Voltcraft Energy-Logger 4000**. It decodes the
`.BIN` files the device writes to its SD card and produces a minute-by-minute
parameter history plus daily, overall and blackout statistics.

Go port of [vbocan/voltcraft-energy-analyzer](https://github.com/vbocan/voltcraft-energy-analyzer)
by Valer Bocan. Standard library only — no third-party dependencies.

## Build

```bash
go build -o energylogger .
```

## Usage

```
energylogger [flags] [<input folder>] [<output folder>]
```

Both folders default to the current directory and may be given either
positionally or by flag. An explicit flag wins over a positional argument.

| Flag | Meaning |
|---|---|
| `-input <dir>` | directory to read Voltcraft `.BIN` files from |
| `-output <dir>` | directory to write the output files to (created if missing) |
| `-quiet` | suppress the banner and per-file progress output |
| `-no-color` | disable ANSI colour (also honours `NO_COLOR` and a non-terminal stdout) |
| `-h`, `--help`, `/?` | show help |

```bash
energylogger /Volumes/SDCARD ~/energy   # read the SD card, write to ~/energy
energylogger /Volumes/SDCARD            # write to the current directory
energylogger                            # read and write in the current directory
```

Every file in the input folder is tried; anything that is not a Voltcraft data
file is reported as `Invalid` and skipped, including the small `INFO:` metadata
file the device also writes. Samples from all files are merged, sorted and
deduplicated by timestamp, so dumping the same SD card twice is harmless.

Exit code is 0 on success, 1 if the output folder is unusable or a file could not
be written, and 2 for a bad command line.

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
Peak power was 1.34kW and occured on [2014-08-26 07:52].
Minute by minute average power: 0.06kW.
...
```

Energy totals only count minutes that were actually recorded, so a day with a
blackout reports the energy used while logging, not an extrapolation. The
per-day "recorded activity" percentage spans the first to the last sample of the
day and therefore includes any gaps in between.

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

The two history files are **byte-identical** to the Rust tool's output. In
`voltcraft_stats.txt` the only differences are the apparent-power lines listed
below.

1. **Fixed:** the daily *Total apparent power* line printed the active-power
   figures (`src/export.rs:181-190` in the original), which is why every day's
   apparent values equalled its active ones in the original README. This port
   prints the real apparent-power totals, averages and peaks.
2. **Fixed:** the overall *Peak power … kVA* line printed the active power of
   the peak-apparent-power sample (`src/export.rs:114`). This port prints its
   apparent power.
3. **Timestamps are treated as UTC wall-clock time** rather than local time.
   The device clock carries no timezone, so this simply prints back what was
   recorded. The original binds each reading to the host timezone, which on a
   DST fall-back day labels one hour twice: for the bundled `sample_data2` it
   prints the 60 minutes of `2014-10-26 02:00`–`02:59` under two different
   instants each. The same choice also means durations, daily grouping and
   blackout lengths no longer depend on the machine's `TZ`.
4. **No panics on damaged input.** A truncated file, a file with no end-of-data
   marker, an impossible date, or an implausible mains voltage is reported as
   invalid and skipped; the original aborted the whole run.
5. The tool's own output files are skipped when the input and output folders are
   the same, so a second run in the same directory does not try to parse them.
6. Per-day statistics are computed in a single pass instead of rescanning every
   sample once per day.

Not changed: the misspelling of "occured" in the statistics file is kept, so the
output still diffs cleanly against the original tool.

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
go test -run TestRunGolden -update .
```

Extremum tie-breaking follows the original deliberately — the last of several
equal maxima and the first of several equal minima — so that reported peak
timestamps stay comparable with the Rust tool's.

## Licence

MIT. This is a derivative work of the Rust original, MIT licensed,
Copyright (c) 2022 Valer BOCAN, PhD, CSSLP. See [LICENSE](LICENSE).
