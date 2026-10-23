package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ry023/entire-timeline/internal/summary"
	"github.com/ry023/entire-timeline/internal/timeline"
)

const summarizeHelp = `Usage: entire timeline summarize <A..B | A...B> [options]

Generate per-message summaries for Copilot conversations in the selected range.
Only user/assistant text is sent to Copilot, together as conversation context.
Validated results are committed locally; this command does not push.
Entire's summary_generation.provider must be copilot-cli.

Options:
  --force                 Regenerate even valid existing summaries
  --batch-bytes N         Input budget before splitting (default: 131072 bytes)
  --checkpoint-ref REF    Additional checkpoint ref (repeatable)
  --verbose               Print restore diagnostics without event values to stderr
  -h, --help              Show help

Large conversations are split on message boundaries with nearby context.
A single oversized message is sent intact. Partial successful results are saved.
`

func repoDir() string {
	if dir := os.Getenv("ENTIRE_REPO_ROOT"); dir != "" {
		return dir
	}
	return "."
}

// parseFlags permits flags on either side of positional arguments, including
// repeatable and --name=value flags. The FlagSet owns value/bool definitions.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			return nil, flag.ErrHelp
		}
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, equal := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown option %s", arg)
		}
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !equal && !(ok && boolean.IsBoolFlag()) {
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s needs a value", arg)
			}
			flags = append(flags, args[i])
		}
	}
	return positional, fs.Parse(flags)
}

type timedGenerator struct {
	summary.Generator
	timeout time.Duration
}

func (g timedGenerator) Generate(ctx context.Context, prompt, model string) ([]byte, error) {
	if g.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.timeout)
		defer cancel()
	}
	return g.Generator.Generate(ctx, prompt, model)
}

func runSummarize(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("summarize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := fs.Bool("force", false, "")
	verbose := fs.Bool("verbose", false, "")
	budget := fs.Int("batch-bytes", summary.DefaultBatchBytes, "")
	var extra refs
	fs.Var(&extra, "checkpoint-ref", "")
	pos, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(stdout, summarizeHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("summarize requires one Git revision range; see summarize --help")
	}
	if *budget <= 0 {
		return fmt.Errorf("--batch-bytes must be positive")
	}
	r, err := summary.Open(ctx, repoDir())
	if err != nil {
		return err
	}
	if err := r.CheckMetadataCheckout(ctx); err != nil {
		return err
	}
	s, err := r.LoadSettings(ctx)
	if err != nil {
		return err
	}
	if err := s.CheckGenerator(); err != nil {
		return err
	}
	t, err := restoreTimeline(ctx, timeline.GitStore{Dir: r.Dir, ExtraRefs: extra}, pos[0], *verbose, stderr)
	if err != nil {
		return err
	}
	for _, warning := range t.Warnings {
		fmt.Fprintln(stderr, "entire-timeline: warning:", warning)
	}
	existing, err := r.Load(ctx, t.Events)
	if err != nil {
		return err
	}
	fmt.Fprintln(stderr, "entire-timeline: Generating conversation summaries...")
	result, genErr := summary.Generate(ctx, t.Events, existing, timedGenerator{summary.Copilot{}, s.Timeout}, summary.Options{Model: s.Model, Force: *force, BatchBytes: *budget})
	// Even cancellation must not throw away already validated response rows.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if saveErr := r.Save(saveCtx, result.Records); saveErr != nil {
		return errors.Join(genErr, fmt.Errorf("save summaries: %w", saveErr))
	}
	fmt.Fprintf(stderr, "entire-timeline: Saved %d summaries locally; reused %d; missing %d (%d Copilot calls).\n", result.Generated, result.Reused, result.Missing, result.Requests)
	return genErr
}

func restoreTimeline(ctx context.Context, store timeline.GitStore, spec string, verbose bool, stderr io.Writer) (timeline.Timeline, error) {
	opt := timeline.RestoreOptions{}
	if verbose {
		opt.Diagnostics = stderr
	}
	return timeline.RestoreWithOptions(ctx, store, spec, opt)
}
