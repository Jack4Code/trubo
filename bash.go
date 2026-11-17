package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	defaultBashTimeout = 120 // seconds
	maxBashTimeout     = 600 // seconds
	maxBashOutput      = 30 * 1024
	// bashWaitDelay is how long trubo waits for the output pipes to close
	// after the command exits or is killed. A background process the command
	// left running can hold them open forever.
	bashWaitDelay = 5 * time.Second
)

// runBash runs a command after the user approves it. Cancelling turnCtx (the
// user pressing Ctrl-C) kills the command's process group, like the timeout.
func runBash(turnCtx context.Context, input json.RawMessage, confirm confirmFunc) (string, error) {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	timeout := defaultBashTimeout
	if args.TimeoutSeconds != nil {
		timeout = *args.TimeoutSeconds
		if timeout < 1 || timeout > maxBashTimeout {
			return "", fmt.Errorf("timeout_seconds must be between 1 and %d", maxBashTimeout)
		}
	}

	fmt.Printf("\nRun in %s:\n  %s\n", workDir, args.Command)
	if !confirm(turnCtx, "Run this command?") {
		return "", errors.New("the user declined to run this command, so nothing was run. " +
			"Ask them what they want instead of retrying the same command")
	}

	ctx, cancel := context.WithTimeout(turnCtx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
	cmd.Dir = workDir
	// Stdin stays nil (reads from /dev/null): trubo's prompt owns the terminal.
	// The command gets its own session, so it has no controlling terminal:
	// anything that opens /dev/tty (sudo, ssh password prompts, git commit
	// opening an editor) fails right away instead of being stopped by the
	// terminal and hanging until the timeout. Setsid also makes the command a
	// process group leader, so the whole group, including anything it
	// started, can still be killed together with syscall.Kill(-pid, ...).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = bashWaitDelay
	var out headTailBuffer
	cmd.Stdout = &out // the same writer for both, so exec serializes the writes
	cmd.Stderr = &out

	err := cmd.Run()
	if cmd.Process != nil {
		// Clean up background processes the command left in its group. Fails
		// harmlessly with ESRCH if the group is already empty.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	output := out.String()
	if output == "" {
		output = "(no output)"
	}
	var exitErr *exec.ExitError
	switch {
	// Check the turn first: cancelling it also cancels ctx.
	case err != nil && turnCtx.Err() != nil:
		return "", fmt.Errorf("%s\n(interrupted by the user; the command was killed)", output)
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("%s\n(timed out after %ds; the command was killed)", output, timeout)
	case errors.Is(err, exec.ErrWaitDelay):
		// The command exited 0 but something it started kept the output open.
		return fmt.Sprintf("%s\n(exit code 0; a background process kept the output open "+
			"and was killed)", output), nil
	case errors.As(err, &exitErr):
		return "", fmt.Errorf("%s\n(exit code %d)", output, exitErr.ExitCode())
	case err != nil:
		return "", fmt.Errorf("running command: %w", err)
	}
	return output + "\n(exit code 0)", nil
}

// headTailBuffer keeps the first and last maxBashOutput/2 bytes written to it
// and counts the rest, so a very chatty command can't use unbounded memory.
type headTailBuffer struct {
	head, tail []byte
	total      int
}

func (b *headTailBuffer) Write(p []byte) (int, error) {
	const half = maxBashOutput / 2
	n := len(p)
	b.total += n
	if room := half - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	b.tail = append(b.tail, p...)
	if len(b.tail) > 2*half {
		b.tail = append([]byte(nil), b.tail[len(b.tail)-half:]...)
	}
	return n, nil
}

func (b *headTailBuffer) String() string {
	const half = maxBashOutput / 2
	tail := b.tail
	if len(tail) > half {
		tail = tail[len(tail)-half:]
	}
	cut := b.total - len(b.head) - len(tail)
	if cut == 0 {
		return string(b.head) + string(tail)
	}
	return fmt.Sprintf("%s\n\n... [%d bytes of output cut from the middle] ...\n\n%s", b.head, cut, tail)
}
