package clipboard

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestResolveCommandSpec_AutoPrefersFirstAvailable(t *testing.T) {
	spec, err := resolveCommandSpec("auto", func(file string) (string, error) {
		switch file {
		case "wl-copy":
			return "/usr/bin/wl-copy", nil
		default:
			return "", errors.New("not found")
		}
	})
	if err != nil {
		t.Fatalf("resolveCommandSpec() error = %v", err)
	}
	if spec.path != "/usr/bin/wl-copy" {
		t.Fatalf("expected wl-copy selected with absolute path, got %q", spec.path)
	}
}

func TestResolveCommandSpec_MethodSpecific(t *testing.T) {
	spec, err := resolveCommandSpec("xclip", func(file string) (string, error) {
		if file == "xclip" {
			return "/usr/bin/xclip", nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatalf("resolveCommandSpec() error = %v", err)
	}
	if spec.path != "/usr/bin/xclip" || len(spec.args) != 2 {
		t.Fatalf("unexpected xclip spec: %#v", spec)
	}
}

func TestResolveCommandSpec_UnsupportedMethod(t *testing.T) {
	_, err := resolveCommandSpec("bad-method", func(file string) (string, error) {
		return "", errors.New("not found")
	})
	if err == nil {
		t.Fatalf("expected unsupported method error")
	}
}

func TestResolveCommandSpec_AvailableNone(t *testing.T) {
	_, err := resolveCommandSpec("auto", func(file string) (string, error) {
		return "", errors.New("not found")
	})
	if !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("expected ErrClipboardUnavailable, got %v", err)
	}
}

func TestNew_Disabled(t *testing.T) {
	_, err := New(Config{Enabled: false, Method: "auto"})
	if !errors.Is(err, ErrClipboardDisabled) {
		t.Fatalf("expected ErrClipboardDisabled, got %v", err)
	}
}

func TestCommandCopier_Copy_NilContextUsesBackground(t *testing.T) {
	c := &commandCopier{}
	if err := c.Copy(nil, "otp"); !errors.Is(err, ErrClipboardUnavailable) {
		t.Fatalf("expected unavailable error for nil copier spec, got %v", err)
	}
	_ = context.Background()
}

func TestCommandCopier_Copy_Success(t *testing.T) {
	c := &commandCopier{
		spec: commandSpec{path: "/fake/helper"},
		commandFn: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return helperCommand(t, ctx, "copy")
		},
	}

	if err := c.Copy(context.Background(), "123456"); err != nil {
		t.Fatalf("expected copy success, got %v", err)
	}
}

func TestCommandCopier_Copy_StartError(t *testing.T) {
	c := &commandCopier{
		spec: commandSpec{path: "/missing/command"},
		commandFn: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "/definitely/not/found/bin")
		},
	}

	err := c.Copy(context.Background(), "123")
	if err == nil {
		t.Fatalf("expected start error")
	}
	if !strings.Contains(err.Error(), "start clipboard command") {
		t.Fatalf("expected start clipboard command error, got %v", err)
	}
}

func TestCommandCopier_Copy_WaitError(t *testing.T) {
	c := &commandCopier{
		spec: commandSpec{path: "/fake/helper"},
		commandFn: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return helperCommand(t, ctx, "fail")
		},
	}

	err := c.Copy(context.Background(), "123")
	if err == nil {
		t.Fatalf("expected wait error")
	}
	if !strings.Contains(err.Error(), "wait clipboard command") {
		t.Fatalf("expected wait clipboard command error, got %v", err)
	}
}

func TestCommandCopier_Copy_Timeout(t *testing.T) {
	c := &commandCopier{
		spec: commandSpec{path: "/fake/helper"},
		commandFn: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return helperCommand(t, ctx, "sleep")
		},
	}

	start := time.Now()
	err := c.Copy(context.Background(), "123")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("copy timeout took too long")
	}
}

func helperCommand(t *testing.T, ctx context.Context, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCommandCopierHelperProcess", "--", mode)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

func TestCommandCopierHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	idx := -1
	for i, arg := range os.Args {
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(os.Args) {
		os.Exit(2)
	}

	mode := os.Args[idx+1]
	switch mode {
	case "copy":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "fail":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(3)
	case "sleep":
		time.Sleep(3 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
