package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"
)

// MaxSocketPath is the portable sun_path limit (macOS 104, Linux 108).
const MaxSocketPath = 104

func Listen(path string) (net.Listener, error) {
	if len(path) > MaxSocketPath {
		return nil, fmt.Errorf("socket path is %d bytes, the limit is %d: %s", len(path), MaxSocketPath, path)
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("a lea daemon already answers on %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
