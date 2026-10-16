// Package wire frames lea's socket protocol: one JSON object per line, at most 1 MiB.
package wire

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
)

const MaxLine = 1 << 20

var ErrTooLong = errors.New("wire: line exceeds 1 MiB")

type Request struct {
	Method string          `json:"method"`
	Agent  string          `json:"agent,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func Write(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLine {
		return ErrTooLong
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func NewReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, MaxLine) }

func Read(r *bufio.Reader, v any) error {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return ErrTooLong
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}
