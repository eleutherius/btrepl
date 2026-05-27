package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Client struct {
	host   string
	client *ssh.Client
}

func New(host, user, keyPath string) (*Client, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}
	addr := net.JoinHostPort(host, "22")
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return &Client{host: host, client: c}, nil
}

// Run executes cmd on the remote host and returns combined output.
func (c *Client) Run(cmd string) (string, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return "", fmt.Errorf("run %q on %s: %w\n%s", cmd, c.host, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// PipeFrom pipes stdout of localCmd into remoteCmd running on the SSH host.
// This is used for: btrfs send ... | ssh host btrfs receive ...
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

	sess.Stdin = pr
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

	localErr := localCmd.Wait()
	// Closing pw signals EOF to the SSH goroutine reading pr, letting
	// btrfs-receive on the remote side finish cleanly before sess.Wait().
	pw.CloseWithError(localErr)
	remoteErr := sess.Wait()

	if localErr != nil {
		return fmt.Errorf("btrfs send: %w", localErr)
	}
	if remoteErr != nil {
		return fmt.Errorf("btrfs receive on %s: %w", c.host, remoteErr)
	}
	return nil
}

// ListDir returns names of entries in dir on the remote host.
func (c *Client) ListDir(dir string) ([]string, error) {
	out, err := c.Run(fmt.Sprintf("ls -1 %s 2>/dev/null || true", dir))
	if err != nil || out == "" {
		return nil, err
	}
	var result []string
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			result = append(result, l)
		}
	}
	return result, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}
