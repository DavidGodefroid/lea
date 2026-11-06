package locks

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HelloAt is the handshake of the console on one socket: Hello, or why there is none.
type HelloAt struct {
	Socket string `json:"socket"`
	Hello  *Hello `json:"hello,omitempty"`
	Err    string `json:"error,omitempty"`
}

// peerUID is the uid of the process serving conn (the kernel's peer credentials); it is a
// variable so tests can stand in for the console account.
var peerUID = socketPeerUID

// SocketNames are the names a console of profile may give its socket in a lock's socket
// directory dir, as lockkit's ipc computes them: <key>-<profile>.sock (or, for a path longer than
// the OS takes, <key>-~<hash of the profile>.sock), the key being the hash of the project root
// root when the console found that project, else "user" (a console without a project marker).
func SocketNames(dir, root, profile string) []string {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return []string{socketName(dir, hex.EncodeToString(sum[:])[:8], profile), socketName(dir, "user", profile)}
}

func socketName(dir, key, profile string) string {
	n := key + "-" + profile + ".sock"
	if len(filepath.Join(dir, n)) <= 107 {
		return n
	}
	ps := sha256.Sum256([]byte(profile))
	return key + "-~" + hex.EncodeToString(ps[:])[:16] + ".sock"
}

var socketNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.~-]{0,100}\.sock$`)

// AskHello sends the hello handshake of protocol major protocol (the one lea's client of the lock
// speaks: its Pin.Protocol) (lockkit's ipc: one JSON-RPC line each way) to the console
// sockets names in dir, or to every *.sock of dir without names (a socket directory its group
// may only traverse, 2710, cannot be listed: lea names the socket), and reports each answer. A
// socket served by another uid than the console account uid is not asked: lea trusts no other
// process's answer. A console of another protocol major refuses the handshake (lockkit's
// "protocol mismatch"): its Hello is then Refused, with the major the console says it speaks. Each socket gets timeout to answer. Only hello is sent: it changes nothing in
// the console.
func AskHello(dir string, names []string, uid, protocol int, timeout time.Duration) ([]HelloAt, error) {
	if len(names) == 0 {
		ents, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrPermission) {
			return nil, errors.New("cannot list " + dir + " (its clients group may only reach a socket it names) and no profile names its socket")
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".sock") && e.Type()&os.ModeSocket != 0 {
				names = append(names, e.Name())
			}
		}
	}
	if protocol < 1 {
		return nil, fmt.Errorf("%d is not a protocol major", protocol)
	}
	var out []HelloAt
	for _, n := range names {
		if !socketNameRe.MatchString(n) || strings.Contains(n, "..") {
			return nil, fmt.Errorf("%q is not a console socket's name", n)
		}
		h, err := askOne(filepath.Join(dir, n), uid, protocol, timeout)
		r := HelloAt{Socket: n, Hello: h}
		if err != nil {
			r.Err = err.Error()
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Socket < out[j].Socket })
	if len(out) == 0 {
		return nil, errors.New("no console socket in " + dir + " (does its console run? lox up)")
	}
	return out, nil
}

func askOne(path string, uid, protocol int, timeout time.Duration) (*Hello, error) {
	c, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, fmt.Errorf("no console answers (%v)", errors.Unwrap(err))
	}
	defer c.Close()
	got, err := peerUID(c)
	if err != nil {
		return nil, err
	}
	if got != uid {
		return nil, fmt.Errorf("served by uid %d, not by the console account (uid %d): not asked", got, uid)
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol_major":%d,"client":"lea-doctor"}}`, protocol) + "\n"
	if _, err := io.WriteString(c, req); err != nil {
		return nil, fmt.Errorf("cannot write the handshake: %v", err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, 1<<16)).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("no answer to the handshake: %v", err)
	}
	var resp struct {
		ID     int64  `json:"id"`
		Result *Hello `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &resp) != nil || resp.ID != 1 {
		return nil, errors.New("not a console's answer to the handshake")
	}
	if resp.Error != nil {
		// A console of another protocol major refuses the handshake, saying so (lockkit's
		// console/server.go hello): it is a mismatch, not an unreadable answer.
		if h := mismatch(resp.Error.Code, resp.Error.Message, protocol); h != nil {
			return h, nil
		}
		return nil, fmt.Errorf("the console refused the handshake: %s", clip(resp.Error.Message))
	}
	if resp.Result == nil || resp.Result.ProtocolMajor < 1 {
		return nil, errors.New("the console's answer names no protocol")
	}
	resp.Result.Refused = false
	resp.Result.ConsoleVersion = clip(resp.Result.ConsoleVersion)
	resp.Result.Profile = clip(resp.Result.Profile)
	return resp.Result, nil
}

// codeInvalidRequest is JSON-RPC's (and lockkit's ipc.CodeInvalidRequest) code of a refused request.
const codeInvalidRequest = -32600

var mismatchRe = regexp.MustCompile(`^protocol mismatch: client speaks (\d+), console speaks (\d+)\b`)

// mismatch is the Refused Hello of lockkit's refusal of a handshake of another protocol major
// (code invalid-request, "protocol mismatch: client speaks N, console speaks M; ..."), the
// console's major M when it says one other than the one lea sent (0 otherwise); nil for any other error.
func mismatch(code int, msg string, sent int) *Hello {
	if code != codeInvalidRequest || !strings.HasPrefix(msg, "protocol mismatch") {
		return nil
	}
	h := &Hello{Refused: true}
	if m := mismatchRe.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil && n >= 1 && n < 1<<16 && n != sent {
			h.ProtocolMajor = n
		}
	}
	return h
}

// clip keeps a console's text printable and short: lea prints it.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
