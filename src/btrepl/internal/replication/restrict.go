package replication

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/eleutherius/btrepl/internal/config"
	"github.com/eleutherius/btrepl/internal/remote"
	"github.com/eleutherius/btrepl/internal/sshclient"
)

// RestrictResult says what RestrictKey did.
type RestrictResult int

const (
	Restricted        RestrictResult = iota // the key now runs only ssh-filter
	AlreadyRestricted                       // ssh-filter already refuses other commands
	NoBtrepl                                // btrepl is not installed on the slave
	KeyNotFound                             // the key is not in ~/.ssh/authorized_keys
)

// ErrRestoreFailed means the restricted key did not work and restoring the
// previous authorized_keys failed too: the master may be locked out.
var ErrRestoreFailed = errors.New("restoring authorized_keys failed")

var safePath = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+$`)

const (
	authorizedKeys = "~/.ssh/authorized_keys"
	backupKeys     = "~/.ssh/authorized_keys.btrepl-bak"
)

// RestrictKey limits the master's key in the slave's authorized_keys to
// `btrepl ssh-filter` from the address the slave sees the master connect
// from. ssh must be an unrestricted connection to the slave. RestrictKey then
// connects again through the restricted key and checks that a replication
// cycle's commands work; if they do not, it restores authorized_keys over
// ssh and returns the error.
func RestrictKey(ssh *sshclient.Client, cfg *config.Config, slave string) (RestrictResult, error) {
	path, err := ssh.Run("command -v btrepl")
	if err != nil {
		if strings.Contains(err.Error(), "command not allowed") {
			return AlreadyRestricted, nil
		}
		return NoBtrepl, nil
	}
	path = strings.TrimSpace(path)
	if !safePath.MatchString(path) {
		return 0, fmt.Errorf("unexpected btrepl path on the slave: %q", path)
	}
	sshClient, err := ssh.Run(`echo "$SSH_CLIENT"`)
	if err != nil {
		return 0, err
	}
	from := clientAddr(sshClient)
	if from == "" {
		return 0, fmt.Errorf("cannot tell the master's address from SSH_CLIENT=%q", sshClient)
	}
	pub, err := publicKey(cfg.SSHIdentity)
	if err != nil {
		return 0, err
	}
	current, err := ssh.Run("cat " + authorizedKeys)
	if err != nil {
		return 0, err
	}
	updated, n := restrictAuthorizedKeys(current+"\n", pub, from, path)
	if n == 0 {
		return KeyNotFound, nil
	}

	write := "umask 077 && cp -p " + authorizedKeys + " " + backupKeys +
		" && cat > " + authorizedKeys + ".btrepl-new && mv " + authorizedKeys + ".btrepl-new " + authorizedKeys
	if _, err := ssh.RunWithInput(write, []byte(updated)); err != nil {
		return 0, fmt.Errorf("write authorized_keys: %w", err)
	}

	if verr := checkRestricted(cfg, slave); verr != nil {
		if _, err := ssh.Run("mv " + backupKeys + " " + authorizedKeys); err != nil {
			return 0, fmt.Errorf("%w: the restricted key does not work (%v), and moving %s back to %s failed: %v",
				ErrRestoreFailed, verr, backupKeys, authorizedKeys, err)
		}
		return 0, fmt.Errorf("the restricted key does not work, authorized_keys restored: %w", verr)
	}
	_, _ = ssh.Run("rm -f " + backupKeys)
	return Restricted, nil
}

// RestrictKeyAndLog runs RestrictKey and logs the outcome. It returns an
// error only when the master may have lost access to the slave.
func RestrictKeyAndLog(ssh *sshclient.Client, cfg *config.Config, slave string, log *slog.Logger) error {
	res, err := RestrictKey(ssh, cfg, slave)
	switch {
	case errors.Is(err, ErrRestoreFailed):
		log.Error("restricting the master's key failed and it could not be restored: fix authorized_keys on the slave by hand",
			"slave", slave, "err", err)
		return err
	case err != nil:
		log.Warn("could not restrict the master's key, it keeps full root access", "slave", slave, "err", err)
	case res == Restricted:
		log.Info("restricted the master's key on the slave to btrepl ssh-filter", "slave", slave)
	case res == AlreadyRestricted:
		log.Info("the master's key on the slave is already restricted", "slave", slave)
	case res == NoBtrepl:
		log.Warn("btrepl is not installed on the slave, so the master's key keeps full root access; install it there and run add-slave again",
			"slave", slave)
	case res == KeyNotFound:
		log.Warn("the master's key is not in ~/.ssh/authorized_keys on the slave; restrict it by hand (see the SSH access docs)",
			"slave", slave)
	}
	return nil
}

// checkRestricted connects to the slave again and runs the commands of a
// replication cycle that change nothing.
func checkRestricted(cfg *config.Config, slave string) error {
	c, err := Dial(cfg, slave, false)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := PrepareSlave(c, cfg); err != nil {
		return err
	}
	_, err = listRemoteSubvolumes(c, remote.New(cfg.BtrfsRoot, cfg.SnapshotDir))
	return err
}

// restrictAuthorizedKeys rewrites every line of content that holds key so
// that it may only run `<btrepl> ssh-filter` from the address from. Other
// lines are kept as they are. It returns the new content and the number of
// lines it rewrote.
func restrictAuthorizedKeys(content string, key ssh.PublicKey, from, btrepl string) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	n := 0
	for i, line := range lines {
		k, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || !bytes.Equal(k.Marshal(), key.Marshal()) {
			continue
		}
		restricted := fmt.Sprintf(`restrict,from="%s",command="%s ssh-filter" %s`,
			from, btrepl, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))))
		if comment != "" {
			restricted += " " + comment
		}
		if strings.HasSuffix(line, "\n") {
			restricted += "\n"
		}
		lines[i] = restricted
		n++
	}
	return strings.Join(lines, ""), n
}

// clientAddr returns the client IP from an SSH_CLIENT value, or "" if it
// does not start with an IP address.
func clientAddr(sshClient string) string {
	fields := strings.Fields(sshClient)
	if len(fields) == 0 || net.ParseIP(fields[0]) == nil {
		return ""
	}
	return fields[0]
}

func publicKey(identity string) (ssh.PublicKey, error) {
	data, err := os.ReadFile(identity)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", identity, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	return signer.PublicKey(), nil
}
