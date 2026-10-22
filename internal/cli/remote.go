package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ry023/entire-timeline/internal/summary"
)

func runRemote(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: entire timeline remote sync\n\nSync saved summaries with Entire's checkpoint remote; no generation.")
		return err
	}
	if len(args) == 0 || args[0] != "sync" {
		return fmt.Errorf("use remote sync; see remote --help")
	}
	fs := flag.NewFlagSet("remote sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fromHook := fs.String("from-hook", "", "")
	pos, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(stdout, "Usage: entire timeline remote sync\n\nPush; on divergence fetch, reapply changed session files, and push again.")
		return err
	}
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("remote sync takes no positional arguments")
	}
	if os.Getenv("ENTIRE_TIMELINE_SYNCING") == "1" {
		return nil
	}
	r, err := summary.Open(ctx, repoDir())
	if err != nil {
		return err
	}
	s, err := r.LoadSettings(ctx)
	if err != nil {
		return err
	}
	state, err := r.Sync(ctx, s, *fromHook)
	if err != nil {
		return err
	}
	w := stdout
	if *fromHook != "" {
		w = stderr
	}
	_, err = fmt.Fprintln(w, state)
	return err
}

func runHooks(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: entire timeline hooks install|uninstall|status\n\nManage a pre-push block to sync saved summaries. Sync failure warns and allows\nthe ordinary push to continue. Reinstall after Entire replaces its hook.")
		return err
	}
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall" && args[0] != "status") {
		return fmt.Errorf("use hooks install, uninstall, or status")
	}
	r, err := summary.Open(ctx, repoDir())
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	state, err := r.Hooks(ctx, args[0], binary)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, state)
	return err
}
