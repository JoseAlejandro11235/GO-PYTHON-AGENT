package python

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cursorlite/internal/meta"
	"cursorlite/internal/paths"
)

const MaxOutputBytes = 512 << 10 // 512 KiB per stream
const MaxStdinBytes = 1 << 20    // 1 MiB for Run stdin

type Output struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

type RunRequest struct {
	Code   string `json:"code"`
	CwdRel string `json:"cwd,omitempty"`
	Stdin  string `json:"stdin,omitempty"`
}

// ResolveWorkDir returns an absolute directory under root for running Python (cwdRel is relative to workspace).
func ResolveWorkDir(root, cwdRel string) (string, error) {
	cwdRel = strings.TrimSpace(cwdRel)
	cwdRel = filepath.ToSlash(filepath.Clean(cwdRel))
	cwdRel = strings.TrimPrefix(cwdRel, "/")
	if cwdRel == "" || cwdRel == "." {
		return root, nil
	}
	full, err := paths.SafeRel(root, cwdRel)
	if err != nil || !paths.UnderRoot(root, full) {
		return "", errors.New("bad cwd")
	}
	st, err := os.Stat(full)
	if err != nil || !st.IsDir() {
		return "", errors.New("cwd is not a directory")
	}
	return full, nil
}

func RunTimeout() time.Duration {
	timeout := 60 * time.Second
	if s := strings.TrimSpace(os.Getenv("PYTHON_RUN_TIMEOUT")); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			timeout = d
		}
	}
	return timeout
}

// PrepareRunScript writes code to workDir/.cursorlite/run.py (hidden from explorer).
func PrepareRunScript(workDir, code string) (scriptPath string, err error) {
	dir := filepath.Join(workDir, meta.CursorliteInternalDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	scriptPath = filepath.Join(dir, meta.CursorliteRunScript)
	if err := os.WriteFile(scriptPath, []byte(code), 0o644); err != nil {
		return "", err
	}
	return scriptPath, nil
}

// RunInWorkspace writes code to a workspace script and runs it so process stdin is free for user input.
func RunInWorkspace(ctx context.Context, root, code, cwdRel, stdin string) (Output, error) {
	code = strings.TrimRight(strings.TrimSpace(code), "\n")
	if code == "" {
		return Output{}, errors.New("code is empty")
	}
	if len(stdin) > MaxStdinBytes {
		return Output{}, errors.New("stdin too large")
	}
	workDir, err := ResolveWorkDir(root, cwdRel)
	if err != nil {
		return Output{}, err
	}
	scriptPath, err := PrepareRunScript(workDir, code)
	if err != nil {
		return Output{}, err
	}
	pythonExe, err := ResolveExecutable()
	if err != nil {
		return Output{Error: err.Error()}, nil
	}
	cmd := exec.CommandContext(ctx, pythonExe, "-u", scriptPath)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{dst: &stdout, max: MaxOutputBytes}
	cmd.Stderr = &limitWriter{dst: &stderr, max: MaxOutputBytes}
	runErr := cmd.Run()
	resp := Output{
		Stdout: strings.TrimSuffix(stdout.String(), "\n"),
		Stderr: strings.TrimSuffix(stderr.String(), "\n"),
	}
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			resp.Error = "python run timed out"
			resp.ExitCode = -1
		} else if ee := (*exec.ExitError)(nil); errors.As(runErr, &ee) {
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.Error = runErr.Error()
			resp.ExitCode = -1
		}
	}
	return resp, nil
}

// RunModule runs `python -m <module> [args...]` with cwd under the workspace.
func RunModule(ctx context.Context, root, cwdRel, module string, args ...string) Output {
	module = strings.TrimSpace(module)
	if module == "" {
		return Output{Error: "module is empty", ExitCode: -1}
	}
	workDir, err := ResolveWorkDir(root, cwdRel)
	if err != nil {
		return Output{Error: err.Error(), ExitCode: -1}
	}
	pythonExe, err := ResolveExecutable()
	if err != nil {
		return Output{Error: err.Error()}
	}
	argv := append([]string{"-m", module}, args...)
	cmd := exec.CommandContext(ctx, pythonExe, argv...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{dst: &stdout, max: MaxOutputBytes}
	cmd.Stderr = &limitWriter{dst: &stderr, max: MaxOutputBytes}
	runErr := cmd.Run()
	resp := Output{
		Stdout: strings.TrimSuffix(stdout.String(), "\n"),
		Stderr: strings.TrimSuffix(stderr.String(), "\n"),
	}
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			resp.Error = "python run timed out"
			resp.ExitCode = -1
		} else if ee := (*exec.ExitError)(nil); errors.As(runErr, &ee) {
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.Error = runErr.Error()
			resp.ExitCode = -1
		}
	}
	return resp
}

type limitWriter struct {
	dst *bytes.Buffer
	max int
	n   int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.n
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		wn, _ := w.dst.Write(p[:remaining])
		w.n += wn
		_, _ = w.dst.WriteString("\n… (output truncated)\n")
		w.n = w.max
		return len(p), nil
	}
	wn, err := w.dst.Write(p)
	w.n += wn
	return len(p), err
}

func ResolveExecutable() (string, error) {
	if p := strings.TrimSpace(os.Getenv("PYTHON_BIN")); p != "" {
		return p, nil
	}
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("python not found on PATH (set PYTHON_BIN or install Python)")
}
