# Test fixtures

Real captures from a Voltcraft Energy-Logger 4000, taken from the sample data of
the Rust original (https://github.com/vbocan/voltcraft-energy-analyzer).

| File | Origin | Why |
|---|---|---|
| `A04FC8D2.BIN` | `sample_data1/A04FC8D2.BIN` | single-block capture; its first sample is the byte fixture the original's unit tests use (2014-09-11 18:43, 224.6 V, 0.446 A, cos φ 0.87) |
| `A04FC8E7.BIN` | `sample_data1/A04FC8E7.BIN` | 12 data blocks, the most of any sample file, so block re-anchoring and the minute reset are exercised |
| `INFO_A060AB85.BIN` | `sample_data2/A060AB85.BIN` | the device's 102-byte `INFO:` metadata file; must be reported invalid and skipped |

`golden/` holds the expected output of running the tool over this directory.
Regenerate it with `go test -run TestRunGolden -update .` and review the diff.
