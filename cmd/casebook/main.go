// Command casebook turns agent traces into evaluation cases.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/matsudan/casebook/evalcase"
	"github.com/matsudan/casebook/importer"
	"github.com/matsudan/casebook/trace"
)

// Exit codes.
const (
	exitFailure = 1 // runtime failure (I/O, bad input, nothing converted)
	exitUsage   = 2 // bad flags or arguments
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func newApp(stdin io.Reader, stdout, stderr io.Writer) *cli.Command {
	return &cli.Command{
		Name:      "casebook",
		Usage:     "Turn agent traces into evaluation cases",
		Reader:    stdin,
		Writer:    stdout,
		ErrWriter: stderr,
		// Exit codes are mapped in run() so the app stays testable.
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Commands: []*cli.Command{
			{
				Name:  "import",
				Usage: "Convert a JSONL trace file into evaluation cases (JSONL)",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "input",
						Aliases: []string{"i"},
						Value:   "-",
						Usage:   `input trace JSONL file ("-" for stdin)`,
					},
					&cli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Value:   "-",
						Usage:   `output case JSONL file ("-" for stdout)`,
					},
				},
				Action: importAction,
			},
		},
	}
}

// run executes the CLI and returns the process exit code.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := newApp(stdin, stdout, stderr)

	// urfave/cli's CommandNotFound hook cannot return an error (and the default
	// behaviour exits with 3), so record the name and report a usage error here.
	var unknown string
	app.CommandNotFound = func(_ context.Context, _ *cli.Command, name string) { unknown = name }

	err := app.Run(context.Background(), append([]string{app.Name}, args...))
	if unknown != "" {
		fmt.Fprintf(stderr, "casebook: unknown command %q (see \"casebook help\")\n", unknown)
		return exitUsage
	}
	if err == nil {
		return 0
	}
	var ec cli.ExitCoder
	if errors.As(err, &ec) {
		fmt.Fprintf(stderr, "casebook: %v\n", ec)
		return ec.ExitCode()
	}
	fmt.Fprintf(stderr, "casebook: %v\n", err)
	return exitUsage
}

func importAction(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Present() {
		return cli.Exit(fmt.Sprintf("unexpected arguments: %v (use -i and -o)", cmd.Args().Slice()), exitUsage)
	}
	root := cmd.Root()
	inPath, outPath := cmd.String("input"), cmd.String("output")

	r := root.Reader
	if inPath != "-" {
		f, err := os.Open(inPath)
		if err != nil {
			return cli.Exit(err, exitFailure)
		}
		defer f.Close()
		r = f
	}

	spans, err := trace.Read(r)
	if err != nil {
		return cli.Exit(fmt.Sprintf("read %s: %v", inPath, err), exitFailure)
	}

	res := importer.FromSpans(spans)
	for _, s := range res.Skipped {
		fmt.Fprintf(root.ErrWriter, "skipped %s\n", s)
	}
	fmt.Fprintf(root.ErrWriter, "%d spans -> %d cases (%d skipped)\n",
		len(spans), len(res.Cases), len(res.Skipped))
	if len(res.Cases) == 0 {
		return cli.Exit("no cases produced", exitFailure)
	}

	if err := writeCases(outPath, root.Writer, res.Cases); err != nil {
		return cli.Exit(err, exitFailure)
	}
	return nil
}

func writeCases(path string, stdout io.Writer, cases []evalcase.Case) (err error) {
	w := stdout
	if path != "-" {
		f, ferr := os.Create(path)
		if ferr != nil {
			return ferr
		}
		defer func() {
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}()
		w = f
	}
	bw := bufio.NewWriter(w)
	if err := evalcase.WriteJSONL(bw, cases); err != nil {
		return err
	}
	return bw.Flush()
}
