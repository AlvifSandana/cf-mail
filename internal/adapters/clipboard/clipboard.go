package clipboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

var ErrClipboardDisabled = errors.New("clipboard disabled")
var ErrClipboardUnavailable = errors.New("clipboard unavailable")

const copyTimeout = 2 * time.Second

type Copier interface {
	Copy(ctx context.Context, text string) error
}

type Config struct {
	Enabled bool
	Method  string
}

type commandSpec struct {
	path string
	args []string
}

type commandCopier struct {
	spec      commandSpec
	commandFn func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

func New(cfg Config) (Copier, error) {
	if !cfg.Enabled {
		return nil, ErrClipboardDisabled
	}

	spec, err := resolveCommandSpec(cfg.Method, exec.LookPath)
	if err != nil {
		return nil, err
	}

	return &commandCopier{spec: spec, commandFn: exec.CommandContext}, nil
}

func (c *commandCopier) Copy(ctx context.Context, text string) error {
	if c == nil {
		return ErrClipboardUnavailable
	}
	if strings.TrimSpace(c.spec.path) == "" {
		return ErrClipboardUnavailable
	}
	if c.commandFn == nil {
		c.commandFn = exec.CommandContext
	}

	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()

	cmd := c.commandFn(ctx, c.spec.path, c.spec.args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open clipboard stdin: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start clipboard command: %w", err)
	}

	_, writeErr := io.WriteString(stdin, text)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if writeErr != nil {
		return fmt.Errorf("write clipboard text: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close clipboard stdin: %w", closeErr)
	}
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return fmt.Errorf("wait clipboard command: %w", waitErr)
	}

	return nil
}

func resolveCommandSpec(method string, lookPath func(file string) (string, error)) (commandSpec, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		method = "auto"
	}

	candidates := []commandSpec{}
	switch method {
	case "auto":
		candidates = []commandSpec{
			{path: "wl-copy"},
			{path: "xclip", args: []string{"-selection", "clipboard"}},
			{path: "xsel", args: []string{"--clipboard", "--input"}},
			{path: "pbcopy"},
			{path: "clip"},
		}
	case "wl-copy":
		candidates = []commandSpec{{path: "wl-copy"}}
	case "xclip":
		candidates = []commandSpec{{path: "xclip", args: []string{"-selection", "clipboard"}}}
	case "xsel":
		candidates = []commandSpec{{path: "xsel", args: []string{"--clipboard", "--input"}}}
	case "pbcopy":
		candidates = []commandSpec{{path: "pbcopy"}}
	case "clip":
		candidates = []commandSpec{{path: "clip"}}
	default:
		return commandSpec{}, fmt.Errorf("clipboard method %q is unsupported (supported: auto, wl-copy, xclip, xsel, pbcopy, clip)", method)
	}

	for _, c := range candidates {
		if p, err := lookPath(c.path); err == nil {
			c.path = p
			return c, nil
		}
	}

	return commandSpec{}, ErrClipboardUnavailable
}
