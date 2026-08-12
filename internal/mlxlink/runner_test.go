package mlxlink

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeFakeMlxlink materialises an executable stand-in for the mlxlink binary.
func writeFakeMlxlink(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "mlxlink")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mlxlink: %v", err)
	}
	return path
}

// killRecordedProcess kills the pid a fake script wrote to pidPath. A missing
// file means the script never got that far, which the test itself reports.
func killRecordedProcess(t *testing.T, pidPath string) {
	t.Helper()

	data, err := os.ReadFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Errorf("read recorded pid: %v", err)
		return
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Errorf("parse recorded pid %q: %v", data, err)
		return
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill recorded pid %d: %v", pid, err)
	}
}

func runErrorFrom(t *testing.T, err error) *RunError {
	t.Helper()

	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("expected *RunError, got %T: %v", err, err)
	}
	return runErr
}

func TestExecRunner_PassesArgumentsAndLocale(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nprintf '{\"args\":\"%s\",\"lc_all\":\"%s\"}' \"$*\" \"$LC_ALL\"\n")
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	out, err := runner.Run(context.Background(), "mlx5_0")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	want := `{"args":"-d mlx5_0 -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --json","lc_all":"C"}`
	if string(out) != want {
		t.Fatalf("expected stdout %s, got %s", want, out)
	}
}

func TestExecRunner_RunWithEyePassesArgumentsAndLocale(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nprintf '{\"args\":\"%s\",\"lc_all\":\"%s\"}' \"$*\" \"$LC_ALL\"\n")
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	out, err := runner.RunWithEye(context.Background(), "mlx5_0")
	if err != nil {
		t.Fatalf("RunWithEye returned error: %v", err)
	}

	want := `{"args":"-d mlx5_0 -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --show_eye --json","lc_all":"C"}`
	if string(out) != want {
		t.Fatalf("expected stdout %s, got %s", want, out)
	}
}

func TestExecRunner_RunPCIeEyePassesArgumentsAndLocale(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nprintf '{\"args\":\"%s\",\"lc_all\":\"%s\"}' \"$*\" \"$LC_ALL\"\n")
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	out, err := runner.RunPCIeEye(context.Background(), "mlx5_0")
	if err != nil {
		t.Fatalf("RunPCIeEye returned error: %v", err)
	}

	want := `{"args":"-d mlx5_0 --port_type PCIE --show_eye --json","lc_all":"C"}`
	if string(out) != want {
		t.Fatalf("expected stdout %s, got %s", want, out)
	}
}

func TestExecRunner_RunBaselinePassesArgumentsAndLocale(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nprintf '{\"args\":\"%s\",\"lc_all\":\"%s\"}' \"$*\" \"$LC_ALL\"\n")
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	out, err := runner.RunBaseline(context.Background(), "mlx5_0")
	if err != nil {
		t.Fatalf("RunBaseline returned error: %v", err)
	}

	want := `{"args":"-d mlx5_0 -m -c --json","lc_all":"C"}`
	if string(out) != want {
		t.Fatalf("expected stdout %s, got %s", want, out)
	}
}

func TestExecRunner_Timeout(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nexec sleep 5\n")
	runner := NewExecRunner(path, 100*time.Millisecond, newDiscardLogger())

	start := time.Now()
	out, err := runner.Run(context.Background(), "mlx5_0")
	elapsed := time.Since(start)

	if out != nil {
		t.Fatalf("expected no output, got %d bytes", len(out))
	}
	if reason := runErrorFrom(t, err).Reason; reason != ReasonTimeout {
		t.Fatalf("expected reason %s, got %s", ReasonTimeout, reason)
	}
	if got := ReasonFromError(err); got != ReasonTimeout {
		t.Fatalf("expected ReasonFromError %s, got %s", ReasonTimeout, got)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("expected the process to be killed at the timeout, took %v", elapsed)
	}
}

func TestExecRunner_ExitError(t *testing.T) {
	t.Parallel()

	script := "#!/bin/sh\nprintf '%s' '" + strings.Repeat("x", 5000) + "' >&2\nexit 3\n"
	path := writeFakeMlxlink(t, script)
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	_, err := runner.Run(context.Background(), "mlx5_0")
	runErr := runErrorFrom(t, err)

	if runErr.Reason != ReasonExitError {
		t.Fatalf("expected reason %s, got %s", ReasonExitError, runErr.Reason)
	}
	if len(runErr.Stderr) != maxStderrBytes {
		t.Fatalf("expected stderr truncated to %d bytes, got %d", maxStderrBytes, len(runErr.Stderr))
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected wrapped *exec.ExitError, got %v", runErr.Err)
	}
	if exitErr.ExitCode() != 3 {
		t.Fatalf("expected exit code 3, got %d", exitErr.ExitCode())
	}
}

func TestExecRunner_CommandNotFound(t *testing.T) {
	t.Parallel()

	runner := NewExecRunner(filepath.Join(t.TempDir(), "mlxlink"), 5*time.Second, newDiscardLogger())

	_, err := runner.Run(context.Background(), "mlx5_0")
	if reason := runErrorFrom(t, err).Reason; reason != ReasonCommandNotFound {
		t.Fatalf("expected reason %s, got %s", ReasonCommandNotFound, reason)
	}
}

func TestExecRunner_PermissionDenied(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mlxlink")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write non-executable mlxlink: %v", err)
	}
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	_, err := runner.Run(context.Background(), "mlx5_0")
	if reason := runErrorFrom(t, err).Reason; reason != ReasonPermissionDenied {
		t.Fatalf("expected reason %s, got %s", ReasonPermissionDenied, reason)
	}
}

func TestExecRunner_OutputTooLarge(t *testing.T) {
	t.Parallel()

	// Floods stdout forever with shell builtins only. Overproduction no longer
	// stops the process, so the timeout is what ends the run; the truncated
	// buffer still outranks the timeout when the failure is labelled.
	script := "#!/bin/sh\nline='" + strings.Repeat("a", 1024) + "'\nwhile :; do printf '%s' \"$line\"; done\n"
	path := writeFakeMlxlink(t, script)
	// The timeout is generous on purpose: the child has to push past the 4 MiB
	// stdout cap before it fires, or the run is labelled a plain timeout. Only
	// a failing run pays the extra seconds.
	runner := NewExecRunner(path, 3*time.Second, newDiscardLogger())

	start := time.Now()
	out, err := runner.Run(context.Background(), "mlx5_0")
	elapsed := time.Since(start)

	if out != nil {
		t.Fatalf("expected no output, got %d bytes", len(out))
	}
	if reason := runErrorFrom(t, err).Reason; reason != ReasonOutputTooLarge {
		t.Fatalf("expected reason %s, got %s", ReasonOutputTooLarge, reason)
	}
	// A process that never stops writing is bounded by the timeout plus the
	// equal WaitDelay, so 6s is the real ceiling; anything past this margin
	// means the caller is no longer bounded at all.
	if elapsed > 8*time.Second {
		t.Fatalf("expected the flooding process to be killed at the timeout, took %v", elapsed)
	}
}

func TestExecRunner_OutputTooLargeOnCleanExit(t *testing.T) {
	t.Parallel()

	// Writes slightly past the limit and exits successfully. The result must be
	// rejected on the buffer state alone, independently of how the process
	// ended.
	script := "#!/bin/sh\nline='" + strings.Repeat("a", 1024) + "'\n" +
		"i=0\nwhile [ $i -lt 4100 ]; do printf '%s' \"$line\"; i=$((i+1)); done\nexit 0\n"
	path := writeFakeMlxlink(t, script)
	runner := NewExecRunner(path, 20*time.Second, newDiscardLogger())

	out, err := runner.Run(context.Background(), "mlx5_0")
	if out != nil {
		t.Fatalf("expected no output, got %d bytes", len(out))
	}
	if reason := runErrorFrom(t, err).Reason; reason != ReasonOutputTooLarge {
		t.Fatalf("expected reason %s, got %s", ReasonOutputTooLarge, reason)
	}
	if !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("expected the failure to name the output limit, got %v", err)
	}
}

func TestExecRunner_LingeringGrandchildDoesNotBlock(t *testing.T) {
	t.Parallel()

	// The direct child exits at once but leaves a grandchild holding the stdout
	// pipe. Without a bounded wait, Run would block on EOF for the grandchild's
	// full lifetime and stall the sweep.
	//
	// The grandchild is orphaned on purpose, so its pid is recorded in a file
	// (stdout belongs to the code under test) and killed when the test ends.
	pidPath := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Cleanup(func() { killRecordedProcess(t, pidPath) })

	path := writeFakeMlxlink(t, "#!/bin/sh\nsleep 30 &\necho $! > \""+pidPath+"\"\nexit 0\n")
	runner := NewExecRunner(path, 300*time.Millisecond, newDiscardLogger())

	start := time.Now()
	_, err := runner.Run(context.Background(), "mlx5_0")
	elapsed := time.Since(start)

	if reason := runErrorFrom(t, err).Reason; reason != ReasonTimeout {
		t.Fatalf("expected reason %s, got %s", ReasonTimeout, reason)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("expected Run to return near the timeout, took %v", elapsed)
	}
}

func TestExecRunner_ParentContextCanceledIsNotRunError(t *testing.T) {
	t.Parallel()

	path := writeFakeMlxlink(t, "#!/bin/sh\nexec sleep 5\n")
	runner := NewExecRunner(path, 5*time.Second, newDiscardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	defer cancel()

	_, err := runner.Run(ctx, "mlx5_0")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	var runErr *RunError
	if errors.As(err, &runErr) {
		t.Fatalf("shutdown must not be reported as a run error, got %v", runErr)
	}
}

func TestReasonFromError_UnknownForPlainError(t *testing.T) {
	t.Parallel()

	if got := ReasonFromError(errors.New("boom")); got != ReasonUnknown {
		t.Fatalf("expected reason %s, got %s", ReasonUnknown, got)
	}
}
