package sshclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// keepaliveInterval is how often New's keepalive checks the connection; it
// is closed after keepaliveMisses intervals without an answer. Tests shorten
// the interval.
var keepaliveInterval = 30 * time.Second

const keepaliveMisses = 3

type Client struct {
	host        string
	client      *ssh.Client
	cmdTimeout  time.Duration
	idleTimeout time.Duration
	stop        chan struct{}
	stopOnce    sync.Once
	// NewHostKey is the SHA256 fingerprint of the host key that New recorded
	// in the known_hosts file, or "" if the host was already known.
	NewHostKey string
}

// Config holds what New needs besides the address.
type Config struct {
	User       string
	KeyPath    string
	KnownHosts string
	// TrustNewHostKey records the key of a host that is not in KnownHosts
	// instead of refusing the connection. A changed key is always refused.
	TrustNewHostKey bool
	// CommandTimeout limits Run and RunWithInput; 0 means no limit.
	CommandTimeout time.Duration
	// SendIdleTimeout aborts PipeFrom when no data has moved for this long;
	// 0 means no limit.
	SendIdleTimeout time.Duration
}

// New connects to addr (host:port) with key authentication and verifies the
// host key against the known_hosts file. A keepalive closes the connection
// when the host stops answering, which makes every pending call fail.
func New(addr string, cfg Config) (*Client, error) {
	key, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	check, err := newHostKeyCheck(cfg.KnownHosts, cfg.TrustNewHostKey)
	if err != nil {
		return nil, err
	}
	sshCfg := &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   check.callback,
		HostKeyAlgorithms: check.algorithms(addr),
		Timeout:           10 * time.Second,
	}
	sc, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	c := &Client{
		host:        addr,
		client:      sc,
		cmdTimeout:  cfg.CommandTimeout,
		idleTimeout: cfg.SendIdleTimeout,
		stop:        make(chan struct{}),
		NewHostKey:  check.recorded,
	}
	go keepalive(keepaliveInterval, keepaliveMisses, func() error {
		_, _, err := sc.SendRequest("keepalive@openssh.com", true, nil)
		return err
	}, func() { sc.Close() }, c.stop)
	return c, nil
}

// keepalive calls send every interval and onDead after misses intervals in a
// row without an answer, until stop is closed.
func keepalive(interval time.Duration, misses int, send func() error, onDead func(), stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		answered := make(chan error, 1)
		go func() { answered <- send() }()
		select {
		case <-stop:
			return
		case err := <-answered:
			if err == nil {
				missed = 0
				continue
			}
			missed = misses // the connection is already gone
		case <-time.After(interval):
			missed++
		}
		if missed >= misses {
			onDead()
			return
		}
	}
}

// Run executes cmd on the remote host and returns its combined output.
func (c *Client) Run(cmd string) (string, error) {
	return c.run(cmd, nil)
}

// RunWithInput executes cmd on the remote host with input on its stdin.
func (c *Client) RunWithInput(cmd string, input []byte) (string, error) {
	return c.run(cmd, input)
}

func (c *Client) run(cmd string, input []byte) (string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	var out lockedBuffer
	sess.Stdout, sess.Stderr = &out, &out
	if input != nil {
		sess.Stdin = bytes.NewReader(input)
	}
	if err := sess.Start(cmd); err != nil {
		return "", fmt.Errorf("run %q on %s: %w", cmd, c.host, err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	var timeout <-chan time.Time
	if c.cmdTimeout > 0 {
		t := time.NewTimer(c.cmdTimeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("run %q on %s: %w\n%s", cmd, c.host, err, out.String())
		}
		return strings.TrimSpace(out.String()), nil
	case <-timeout:
		_ = sess.Signal(ssh.SIGKILL)
		sess.Close()
		return "", fmt.Errorf("run %q on %s: no result after %s", cmd, c.host, c.cmdTimeout)
	}
}

// lockedBuffer is a bytes.Buffer that stdout and stderr can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var errStalled = errors.New("send stalled")

// PipeFrom pipes stdout of localCmd into remoteCmd running on the SSH host.
// This is used for: btrfs send ... | ssh host btrfs receive ...
// If no data reaches the host for the SendIdleTimeout, both sides are
// stopped and an error is returned.
func (c *Client) PipeFrom(localCmd *exec.Cmd, remoteCmd string) error {
	sess, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	// Use an explicit io.Pipe so we control when EOF is sent to the remote.
	// exec.Cmd.StdoutPipe() closes the pipe inside Wait(), which races with
	// the SSH goroutine still draining buffered data and causes
	// "read |0: file already closed" on the second subvolume.
	pr, pw := io.Pipe()
	localCmd.Stdout = pw
	localCmd.Stderr = os.Stderr

	sent := &countingReader{r: pr}
	sess.Stdin = sent
	sess.Stdout = os.Stdout
	sess.Stderr = os.Stderr

	if err := sess.Start(remoteCmd); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("start remote %q: %w", remoteCmd, err)
	}
	if err := localCmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		sess.Wait() //nolint:errcheck
		return fmt.Errorf("start local cmd: %w", err)
	}

	remoteDone := make(chan error, 1)
	go func() { remoteDone <- sess.Wait() }()

	var stalled atomic.Bool
	stalledCh := make(chan struct{})
	if c.idleTimeout > 0 {
		stop := watchProgress(sent.n.Load, c.idleTimeout, func() {
			stalled.Store(true)
			close(stalledCh)
			// Closing the reader unblocks the copy into pw, so that
			// localCmd.Wait returns after the kill.
			pr.CloseWithError(errStalled)
			_ = localCmd.Process.Kill()
			_ = sess.Signal(ssh.SIGKILL)
			sess.Close()
		})
		defer stop()
	}

	localErr := localCmd.Wait()
	// Closing pw signals EOF to the SSH goroutine reading pr, letting
	// btrfs-receive on the remote side finish cleanly.
	pw.CloseWithError(localErr)
	var remoteErr error
	select {
	case remoteErr = <-remoteDone:
	case <-stalledCh:
		// OpenSSH keeps the channel open until the remote process exits,
		// so do not wait for it after giving up.
	}

	if stalled.Load() {
		return fmt.Errorf("send to %s made no progress for %s", c.host, c.idleTimeout)
	}
	if localErr != nil {
		return fmt.Errorf("btrfs send: %w", localErr)
	}
	if remoteErr != nil {
		return fmt.Errorf("btrfs receive on %s: %w", c.host, remoteErr)
	}
	return nil
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// watchProgress calls onStall once if progress() stays the same for idle.
// It covers the whole transfer, including the wait for the receiver to exit
// after the last byte.
func watchProgress(progress func() int64, idle time.Duration, onStall func()) (stop func()) {
	done := make(chan struct{})
	check := min(max(idle/10, 10*time.Millisecond), 30*time.Second)
	go func() {
		ticker := time.NewTicker(check)
		defer ticker.Stop()
		last, since := progress(), time.Now()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			if n := progress(); n != last {
				last, since = n, time.Now()
			} else if time.Since(since) >= idle {
				onStall()
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (c *Client) Close() error {
	c.stopOnce.Do(func() { close(c.stop) })
	return c.client.Close()
}
