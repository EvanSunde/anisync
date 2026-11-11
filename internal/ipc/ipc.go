// Package ipc is a tiny JSON request/response protocol over a Unix socket,
// used by the CLI to talk to the daemon.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"time"
)

type Request struct {
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

var ErrNotRunning = errors.New("daemon is not running (start it with: systemctl --user start anisync)")

func Serve(path string, h func(Request) (any, error)) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, errors.New("another anisync daemon is already running")
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go handle(c, h)
		}
	}()
	return l, nil
}

func handle(c net.Conn, h func(Request) (any, error)) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req Request
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&req); err != nil {
		return
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	var resp Response
	data, err := h(req)
	if err != nil {
		resp.Error = err.Error()
	} else if data != nil {
		resp.Data, _ = json.Marshal(data)
	}
	if err := json.NewEncoder(c).Encode(resp); err != nil {
		log.Printf("ipc: %v", err)
	}
}

func Call(path, cmd string, args, out any) error {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return ErrNotRunning
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))
	req := Request{Cmd: cmd}
	if args != nil {
		req.Args, _ = json.Marshal(args)
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return err
	}
	var resp Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}
	return nil
}
