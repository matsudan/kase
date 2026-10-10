package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	const (
		otlpFixture   = "../../testdata/otlp_latest.jsonl"
		legacyFixture = "../../testdata/strands_legacy.jsonl"
	)

	tests := []struct {
		name      string
		args      []string
		stdin     string // literal stdin; ignored when stdinFile is set
		stdinFile string // file whose contents are fed to stdin
		outFile   bool   // append "-o <tmp file>" and read cases from it

		wantCode int
		// wantCases is the number of JSONL cases expected in the output (the -o
		// file, or stdout). It is checked only when wantStdout is empty.
		wantCases  int
		wantStdout string // substring of stdout, for non-case output such as help
		wantStderr string // substring of stderr
	}{
		{
			name:       "file to file",
			args:       []string{"import", "-i", otlpFixture},
			outFile:    true,
			wantCases:  1,
			wantStderr: "3 spans -> 1 cases (0 skipped)",
		},
		{
			name:       "stdin to stdout",
			args:       []string{"import"},
			stdinFile:  legacyFixture,
			wantCases:  1,
			wantStderr: "3 spans -> 1 cases (0 skipped)",
		},
		{
			name:       "explicit dash for stdin and stdout",
			args:       []string{"import", "--input", "-", "--output", "-"},
			stdinFile:  legacyFixture,
			wantCases:  1,
			wantStderr: "3 spans -> 1 cases",
		},
		{
			name:       "no args prints help",
			args:       nil,
			wantStdout: "COMMANDS",
		},
		{
			name:       "import help",
			args:       []string{"import", "--help"},
			wantStdout: "--input",
		},
		{
			name:       "unknown command",
			args:       []string{"nope"},
			wantCode:   exitUsage,
			wantStderr: `unknown command "nope"`,
		},
		{
			name:     "undefined flag",
			args:     []string{"import", "--bogus"},
			wantCode: exitUsage,
			// urfave/cli prints the subcommand usage to stdout on flag errors.
			wantStdout: "USAGE:",
			wantStderr: "flag provided but not defined: -bogus",
		},
		{
			name:       "stray positional argument",
			args:       []string{"import", "stray.jsonl"},
			wantCode:   exitUsage,
			wantStderr: "unexpected arguments: [stray.jsonl]",
		},
		{
			name:       "missing input file",
			args:       []string{"import", "-i", "/does/not/exist"},
			wantCode:   exitFailure,
			wantStderr: "no such file or directory",
		},
		{
			name:       "unwritable output path",
			args:       []string{"import", "-i", otlpFixture, "-o", "/does/not/exist/cases.jsonl"},
			wantCode:   exitFailure,
			wantStderr: "no such file or directory",
		},
		{
			name:       "malformed input",
			args:       []string{"import"},
			stdin:      "{bad",
			wantCode:   exitFailure,
			wantStderr: "read -: line 1:",
		},
		{
			name:       "valid spans but nothing convertible",
			args:       []string{"import"},
			stdin:      `{"traceId":"t","spanId":"s","name":"chat"}`,
			wantCode:   exitFailure,
			wantStderr: "no cases produced",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdin := []byte(tt.stdin)
			if tt.stdinFile != "" {
				b, err := os.ReadFile(tt.stdinFile)
				if err != nil {
					t.Fatal(err)
				}
				stdin = b
			}
			args := tt.args
			var outPath string
			if tt.outFile {
				outPath = filepath.Join(t.TempDir(), "cases.jsonl")
				args = append(append([]string(nil), args...), "-o", outPath)
			}

			var stdout, stderr bytes.Buffer
			code := run(args, bytes.NewReader(stdin), &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d\nstderr: %s", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want containing %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantStdout != "" {
				if !strings.Contains(stdout.String(), tt.wantStdout) {
					t.Errorf("stdout = %q, want containing %q", stdout.String(), tt.wantStdout)
				}
				return
			}

			out := stdout.Bytes()
			if tt.outFile {
				if stdout.Len() != 0 {
					t.Errorf("stdout must be empty when -o is set, got %q", stdout.String())
				}
				var err error
				if out, err = os.ReadFile(outPath); err != nil {
					t.Fatal(err)
				}
			}
			if got := countCases(t, out); got != tt.wantCases {
				t.Errorf("output has %d cases, want %d:\n%s", got, tt.wantCases, out)
			}
		})
	}
}

// countCases checks that every line of out is a case object and counts them.
func countCases(t *testing.T, out []byte) int {
	t.Helper()
	n := 0
	for line := range strings.Lines(string(out)) {
		var c struct {
			ID            string `json:"id"`
			SchemaVersion int    `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("output line is not a JSON object: %v\n%s", err, line)
		}
		if c.SchemaVersion != 1 || !strings.HasPrefix(c.ID, "sha256:") {
			t.Errorf("unexpected case header: %+v", c)
		}
		n++
	}
	return n
}
