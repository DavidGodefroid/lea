package wire

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Request{Method: "msg_send", Agent: "w1:p1", Params: []byte(`{"to":"x"}`)}
	if err := Write(&buf, in); err != nil {
		t.Fatal(err)
	}
	var out Request
	if err := Read(NewReader(&buf), &out); err != nil {
		t.Fatal(err)
	}
	if out.Method != in.Method || out.Agent != in.Agent || string(out.Params) != `{"to":"x"}` {
		t.Fatalf("got %+v", out)
	}
}

func TestWriteTooLong(t *testing.T) {
	big := Request{Method: strings.Repeat("a", MaxLine)}
	if err := Write(&bytes.Buffer{}, big); !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

func TestReadTooLong(t *testing.T) {
	line := strings.Repeat("a", MaxLine+10) + "\n"
	var v Request
	if err := Read(NewReader(strings.NewReader(line)), &v); !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}
