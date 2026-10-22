package summary

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Copilot struct{}

// Generate follows Entire's text-only Copilot invocation. Prompt bytes never
// appear in argv. Keep partial stdout on failure for per-row validation.
func (Copilot) Generate(ctx context.Context, prompt, model string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "entire-timeline-copilot-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	args := []string{"--deny-tool", "shell", "--deny-tool", "write", "--deny-tool", "url", "--no-ask-user", "--no-custom-instructions", "--disable-builtin-mcps", "--available-tools=entire_text_generation_uses_no_tools", "-s", "--stream", "off", "--disallow-temp-dir", "--no-auto-update"}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "copilot", args...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "ENTIRE_") || upper == "PWD" || upper == "OLDPWD" || upper == "COPILOT_ALLOW_ALL" || upper == "COPILOT_AGENT_SESSION_ID" {
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	cmd.Env = append(cmd.Env, "PWD="+dir)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 2048 {
			detail = detail[:2048] + "…"
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return stdout.Bytes(), fmt.Errorf("copilot text generation: %w: %s", err, detail)
	}
	return stdout.Bytes(), nil
}
