// Package client sends one request to a lea socket and reads one response.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	"github.com/DavidGodefroid/lea/internal/wire"
)

func Call(ctx context.Context, sock string, req wire.Request, result any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Cancelling ctx closes the socket, which ends a blocked read and cancels the daemon's request.
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if err := wire.Write(conn, req); err != nil {
		return err
	}
	var resp wire.Response
	if err := wire.Read(wire.NewReader(conn), &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result, result)
}
