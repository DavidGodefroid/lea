// Package acp is a minimal Agent Client Protocol client, enough to check that a vendor CLI runs
// headless in a lea image: initialize, open a session, send one prompt, report the stop reason.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// maxLine caps one JSON-RPC line from the (untrusted) child.
const maxLine = 8 << 20

func readLine(rd *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := rd.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLine {
			return nil, fmt.Errorf("agent line exceeds %d bytes", maxLine)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}

// syncWriter serialises the child's stderr copy and our own log lines onto one writer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func Probe(ctx context.Context, cmd []string, cwd, prompt string, log io.Writer) (string, error) {
	log = &syncWriter{w: log}
	cwd, err := filepath.Abs(cwd) // ACP requires an absolute session cwd
	if err != nil {
		return "", err
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Dir = cwd
	stdin, err := c.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return "", err
	}
	c.Stderr = log
	// A grandchild that inherited stderr must not keep Wait blocked after the child is killed.
	c.WaitDelay = 2 * time.Second
	if err := c.Start(); err != nil {
		return "", err
	}
	defer func() {
		stdin.Close()
		c.Process.Kill()
		c.Wait() // reap the child and finish the stderr copy before returning
	}()
	rd := bufio.NewReaderSize(stdout, 1<<20)
	enc := json.NewEncoder(stdin)
	var next int64
	request := func(method string, params any) (json.RawMessage, error) {
		next++
		id := strconv.FormatInt(next, 10)
		p, _ := json.Marshal(params)
		if err := enc.Encode(msg{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: p}); err != nil {
			return nil, err
		}
		for {
			line, err := readLine(rd)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
			var m msg
			if json.Unmarshal(line, &m) != nil {
				fmt.Fprintf(log, "non-JSON line: %s", line)
				continue
			}
			switch {
			case m.Method == "session/update":
				fmt.Fprintf(log, "update: %s\n", m.Params)
			case m.Method == "session/request_permission" && len(m.ID) > 0:
				fmt.Fprintf(log, "permission asked: %s\n", m.Params)
				enc.Encode(msg{JSONRPC: "2.0", ID: m.ID, Result: json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`)})
			case m.Method != "" && len(m.ID) > 0:
				fmt.Fprintf(log, "unsupported request %s\n", m.Method)
				enc.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported by lea acp-probe"}})
			case m.Method == "" && string(m.ID) == id:
				if m.Error != nil {
					return nil, fmt.Errorf("%s: %d %s", method, m.Error.Code, m.Error.Message)
				}
				return m.Result, nil
			}
		}
	}
	if _, err := request("initialize", map[string]any{"protocolVersion": 1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false}}); err != nil {
		return "", err
	}
	res, err := request("session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return "", err
	}
	var s struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &s); err != nil || s.SessionID == "" {
		return "", fmt.Errorf("session/new returned no sessionId: %s", res)
	}
	res, err = request("session/prompt", map[string]any{"sessionId": s.SessionID,
		"prompt": []map[string]string{{"type": "text", "text": prompt}}})
	if err != nil {
		return "", err
	}
	var r struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", fmt.Errorf("session/prompt result: %w", err)
	}
	if r.StopReason == "" {
		return "", fmt.Errorf("session/prompt returned no stopReason: %s", res)
	}
	return r.StopReason, nil
}
