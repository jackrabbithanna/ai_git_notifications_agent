package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Event is one JSON line pi wrote to stdout (an agent event or an unsolicited response).
type Event map[string]any

// Type returns the event's "type" field.
func (e Event) Type() string {
	t, _ := e["type"].(string)
	return t
}

// String returns a string field ("" when missing).
func (e Event) String(key string) string {
	s, _ := e[key].(string)
	return s
}

// Response is pi's reply to a command with an id.
type Response struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// Client speaks pi's RPC protocol over a child process's stdin/stdout.
type Client struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu      sync.Mutex
	nextID  int
	pending map[string]chan Response
	closed  bool

	done    chan struct{}
	exitErr error
}

// SpawnOptions describe the process to start.
type SpawnOptions struct {
	Path    string
	Args    []string
	Env     []string // full environment
	Dir     string
	Stderr  io.Writer
	OnEvent func(Event)
	OnExit  func(err error)
}

// Spawn starts pi and begins reading its stdout.
func Spawn(ctx context.Context, opts SpawnOptions) (*Client, error) {
	cmd := exec.CommandContext(ctx, opts.Path, opts.Args...)
	cmd.Env = opts.Env
	cmd.Dir = opts.Dir
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cmd: cmd, stdin: stdin, pending: map[string]chan Response{}, done: make(chan struct{})}
	go c.read(stdout, opts.OnEvent)
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.exitErr = err
		c.closed = true
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.done)
		if opts.OnExit != nil {
			opts.OnExit(err)
		}
	}()
	return c, nil
}

func (c *Client) read(r io.Reader, onEvent func(Event)) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			var ev Event
			if jerr := json.Unmarshal(line, &ev); jerr == nil {
				if ev.Type() == "response" {
					if id, ok := ev["id"].(string); ok && id != "" {
						var resp Response
						_ = json.Unmarshal(line, &resp)
						c.mu.Lock()
						ch := c.pending[id]
						delete(c.pending, id)
						c.mu.Unlock()
						if ch != nil {
							ch <- resp
							close(ch)
						}
						continue
					}
				}
				if onEvent != nil {
					onEvent(ev)
				}
			} else if onEvent != nil {
				onEvent(Event{"type": "stdout", "text": string(line)})
			}
		}
		if err != nil {
			return
		}
	}
}

// Send writes one command without waiting for a response.
func (c *Client) Send(cmd map[string]any) error {
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("pi is not running")
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// Call sends a command with an id and waits for its response.
func (c *Client) Call(ctx context.Context, cmd map[string]any) (Response, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Response{}, errors.New("pi is not running")
	}
	c.nextID++
	id := "c" + strconv.Itoa(c.nextID)
	ch := make(chan Response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	cmd["id"] = id
	if err := c.Send(cmd); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Response{}, err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return Response{}, errors.New("pi exited before responding")
		}
		if !resp.Success {
			return resp, fmt.Errorf("pi %s: %s", resp.Command, resp.Error)
		}
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Response{}, ctx.Err()
	}
}

// Close ends the process: stdin is closed, then the process is killed if it
// does not exit within a short grace period.
func (c *Client) Close() error {
	c.mu.Lock()
	already := c.closed
	c.mu.Unlock()
	_ = c.stdin.Close()
	if already {
		return nil
	}
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.done
		}
	}
	return nil
}

// Done is closed when the process has exited.
func (c *Client) Done() <-chan struct{} { return c.done }

// ExitError is the process's exit error (nil while running or on clean exit).
func (c *Client) ExitError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exitErr
}
