package sshclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// testServer is an in-process SSH server for the client tests. Exec requests
// run fake commands: "echo-stdin" copies stdin to stdout, "hang" never
// finishes and never reads stdin, anything else prints its command line.
type testServer struct {
	addr       string
	cfg        Config
	unresponse atomic.Bool // stop answering global requests (keepalives)
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	clientPub, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "id")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	authorized, _ := ssh.NewPublicKey(clientPub)

	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(k.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	srvCfg.AddHostKey(hostSigner)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lis.Close() })
	s := &testServer{addr: lis.Addr().String()}

	known := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr)}, hostSigner.PublicKey())
	if err := os.WriteFile(known, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg = Config{User: "root", KeyPath: keyPath, KnownHosts: known}

	go func() {
		for {
			nc, err := lis.Accept()
			if err != nil {
				return
			}
			go s.handle(nc, srvCfg)
		}
	}()
	return s
}

func (s *testServer) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go func() {
		for r := range reqs {
			if !s.unresponse.Load() && r.WantReply {
				r.Reply(false, nil)
			}
		}
	}()
	for nch := range chans {
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go session(ch, creqs)
	}
}

func session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for r := range reqs {
		if r.Type != "exec" {
			r.Reply(false, nil)
			continue
		}
		var p struct{ Command string }
		ssh.Unmarshal(r.Payload, &p)
		r.Reply(true, nil)
		switch p.Command {
		case "hang":
			continue
		case "echo-stdin":
			io.Copy(ch, ch)
		default:
			io.WriteString(ch, p.Command)
		}
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		ch.Close()
	}
}

func dial(t *testing.T, s *testServer, cfg Config) *Client {
	t.Helper()
	c, err := New(s.addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRunAndRunWithInput(t *testing.T) {
	s := newTestServer(t)
	c := dial(t, s, s.cfg)
	if out, err := c.Run("hello world"); err != nil || out != "hello world" {
		t.Errorf("Run = %q, %v", out, err)
	}
	if out, err := c.RunWithInput("echo-stdin", []byte("some input\n")); err != nil || out != "some input" {
		t.Errorf("RunWithInput = %q, %v", out, err)
	}
}

func TestRunTimeout(t *testing.T) {
	s := newTestServer(t)
	cfg := s.cfg
	cfg.CommandTimeout = 200 * time.Millisecond
	c := dial(t, s, cfg)

	start := time.Now()
	_, err := c.Run("hang")
	if err == nil || !strings.Contains(err.Error(), "no result after") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Run returned after %s", d)
	}
	// The connection is still usable for the next command.
	if out, err := c.Run("next"); err != nil || out != "next" {
		t.Errorf("Run after timeout = %q, %v", out, err)
	}
}

func TestPipeFrom(t *testing.T) {
	s := newTestServer(t)
	cfg := s.cfg
	cfg.SendIdleTimeout = 5 * time.Second
	c := dial(t, s, cfg)
	if err := c.PipeFrom(exec.Command("echo", "stream"), "echo-stdin"); err != nil {
		t.Fatal(err)
	}
}

func TestPipeFromAbortsStalledSend(t *testing.T) {
	s := newTestServer(t)
	cfg := s.cfg
	cfg.SendIdleTimeout = 300 * time.Millisecond
	c := dial(t, s, cfg)

	start := time.Now()
	err := c.PipeFrom(exec.Command("yes"), "hang") // the receiver never reads
	if err == nil || !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("PipeFrom returned after %s", d)
	}
}

func TestKeepaliveClosesDeadConnection(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 20 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = old })

	s := newTestServer(t)
	c := dial(t, s, s.cfg)
	if _, err := c.Run("alive"); err != nil {
		t.Fatal(err)
	}
	s.unresponse.Store(true)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Run("alive"); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("connection to an unresponsive server was not closed")
}

func TestWatchProgress(t *testing.T) {
	var n atomic.Int64
	stalled := make(chan struct{})
	stop := watchProgress(n.Load, 100*time.Millisecond, func() { close(stalled) })
	defer stop()

	// Progress keeps the watchdog quiet.
	for i := 0; i < 10; i++ {
		n.Add(1)
		select {
		case <-stalled:
			t.Fatal("stalled while making progress")
		case <-time.After(30 * time.Millisecond):
		}
	}
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("no stall reported without progress")
	}
}
