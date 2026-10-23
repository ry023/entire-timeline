package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ry023/entire-timeline/internal/summary"
	"github.com/ry023/entire-timeline/internal/timeline"
)

const help = `Usage: entire-timeline <A..B | A...B> [options]
       entire timeline <A..B | A...B> [options]
       entire timeline summarize <range> [--force] [--batch-bytes N]
       entire timeline remote sync
       entire timeline hooks install|uninstall|status

Restore Entire conversations with saved summaries from local Git (read-only).
A...B means merge-base(A,B)..B. Options may precede or follow the range.

Options:
  --include-tools          Print complete tool calls/results and original events
  --format text|jsonl       Output format (default: text)
  --output PATH            Write output to a file (default: stdout; '-' = stdout)
  --checkpoint-ref REF     Additional metadata branch/ref (repeatable)
  --width N                Separator/tool summary width (default: COLUMNS or 80)
  --verbose                Print restore diagnostics without event values to stderr
  -h, --help               Show help

Supported agents: Copilot CLI, Claude Code. Requires Git; no Entire CLI or API
is required for direct execution. Missing local checkpoints are an error.
`

type refs []string

func (r *refs) String() string     { return strings.Join(*r, ",") }
func (r *refs) Set(s string) error { *r = append(*r, s); return nil }

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if err := run(ctx, args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "entire-timeline:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "summarize" {
		return runSummarize(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "remote" {
		return runRemote(ctx, args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "hooks" {
		return runHooks(ctx, args[1:], stdout)
	}
	fs := flag.NewFlagSet("entire-timeline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "")
	output := fs.String("output", "", "")
	tools := fs.Bool("include-tools", false, "")
	verbose := fs.Bool("verbose", false, "")
	widthDefault := 80
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		widthDefault = n
	}
	width := fs.Int("width", widthDefault, "")
	var extra refs
	fs.Var(&extra, "checkpoint-ref", "")
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			_, err := io.WriteString(stdout, help)
			return err
		}
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		key, _, equal := strings.Cut(arg, "=")
		if !equal && (key == "--format" || key == "--output" || key == "--width" || key == "--checkpoint-ref") {
			i++
			if i >= len(args) {
				return fmt.Errorf("%s needs a value", arg)
			}
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("provide one Git revision range (A..B or A...B); see --help")
	}
	if *format != "text" && *format != "jsonl" {
		return fmt.Errorf("unsupported format %q; use text or jsonl", *format)
	}
	if *width < 1 {
		return fmt.Errorf("--width must be positive")
	}
	dir := os.Getenv("ENTIRE_REPO_ROOT")
	if dir == "" {
		dir = "."
	}
	t, err := restoreTimeline(ctx, timeline.GitStore{Dir: dir, ExtraRefs: extra}, positionals[0], *verbose, stderr)
	if err != nil {
		return err
	}
	r, err := summary.Open(ctx, dir)
	if err != nil {
		return err
	}
	rows, err := r.Load(ctx, t.Events)
	if err != nil {
		return err
	}
	summary.Attach(&t, rows)
	for _, warning := range t.Warnings {
		fmt.Fprintln(stderr, "entire-timeline: warning:", warning)
	}
	render := func(w io.Writer) error {
		if *format == "jsonl" {
			return timeline.RenderJSONL(w, t)
		}
		return timeline.RenderText(w, t, timeline.TextOptions{IncludeTools: *tools, Width: *width})
	}
	if *output == "" || *output == "-" {
		return render(stdout)
	}
	// Render to a private sibling first: a restore/render failure must not
	// truncate an existing export, and a successful rename is atomic.
	f, err := os.CreateTemp(filepath.Dir(*output), ".entire-timeline-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := render(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), *output); err != nil {
		return err
	}
	return nil
}
