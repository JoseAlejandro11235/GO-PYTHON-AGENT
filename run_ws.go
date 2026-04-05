package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // local dev; tighten if exposed to untrusted origins
	},
}

type wsClientMsg struct {
	Type string `json:"type"`
	Code string `json:"code,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	Line string `json:"line,omitempty"`
}

type wsServerMsg struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func handleRunPythonWS(w http.ResponseWriter, r *http.Request, root string) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	_, startRaw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var start wsClientMsg
	if err := json.Unmarshal(startRaw, &start); err != nil || start.Type != "start" {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: "first message must be {\"type\":\"start\",\"code\":\"...\",\"cwd\":\"\"}"})
		return
	}
	code := strings.TrimRight(strings.TrimSpace(start.Code), "\n")
	if code == "" {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: "code is empty"})
		return
	}

	workDir, err := resolveWorkspaceWorkDir(root, start.Cwd)
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}
	scriptPath, err := prepareCursorliteRunScript(workDir, code)
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}
	pythonExe, err := resolvePythonExecutable()
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pythonRunTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonExe, "-u", scriptPath)
	cmd.Dir = workDir
	stdinW, err := cmd.StdinPipe()
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}
	if err := cmd.Start(); err != nil {
		_ = conn.WriteJSON(wsServerMsg{Type: "error", Message: err.Error()})
		return
	}

	var wsMu sync.Mutex
	write := func(m wsServerMsg) {
		wsMu.Lock()
		defer wsMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteJSON(m)
	}

	var outBytes atomic.Int64
	pump := func(r io.Reader, streamType string) {
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				if outBytes.Add(int64(n)) > maxPythonOutput {
					cancel()
					return
				}
				write(wsServerMsg{Type: streamType, Data: string(buf[:n])})
			}
			if rerr != nil {
				return
			}
		}
	}
	go pump(stdoutR, "stdout")
	go pump(stderrR, "stderr")

	var stdinMu sync.Mutex
	var stdinOnce sync.Once
	closeStdin := func() {
		stdinOnce.Do(func() {
			stdinMu.Lock()
			defer stdinMu.Unlock()
			_ = stdinW.Close()
		})
	}

	go func() {
		defer closeStdin()
		for {
			_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			_, raw, rerr := conn.ReadMessage()
			if rerr != nil {
				cancel()
				return
			}
			var msg wsClientMsg
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "stdin":
				line := msg.Line
				if len(line) > 1<<16 {
					continue
				}
				stdinMu.Lock()
				_, _ = io.WriteString(stdinW, line)
				if !strings.HasSuffix(line, "\n") {
					_, _ = stdinW.Write([]byte{'\n'})
				}
				stdinMu.Unlock()
			case "cancel":
				cancel()
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	closeStdin()

	var exitCode int
	if waitErr != nil {
		if ee := (*exec.ExitError)(nil); errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			exitCode = -1
		} else {
			exitCode = -1
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		write(wsServerMsg{Type: "stderr", Data: "\n— Run timed out\n"})
	}
	write(wsServerMsg{Type: "exit", Code: exitCode})
}
