# SSH access

The master connects to every slave over SSH as `ssh_user` (default `root`) with the key in `ssh_identity`. Only key authentication is used.

## Host keys

The master checks each slave's host key against `ssh_known_hosts` (default `/root/.ssh/known_hosts`), the same file and format OpenSSH uses.

- `btrepl add-slave -s <host>` records the slave's key if the file does not have it yet, and logs its SHA256 fingerprint. Compare it with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the slave.
- Replication cycles and `status -s` refuse a slave whose key is not in the file.
- A key that differs from the recorded one is always refused, because it can mean a man-in-the-middle attack. If the slave was reinstalled, remove the old key and add the slave again:

  ```bash
  ssh-keygen -R <host> -f /root/.ssh/known_hosts
  btrepl add-slave -s <host>
  ```

Keys recorded by `ssh-keyscan` or by connecting once with `ssh` work as well.

!!! note "Upgrading from 0.1"
    Earlier versions did not check host keys. If a slave's key is not in `known_hosts` yet, run `btrepl add-slave -s <host>` once for it. The slave is already in the config, so this only records its key.

## Port

Slaves listen on `ssh_port` (default `22`). To use another port for one slave, add it as `host:port`, or `[address]:port` for IPv6:

```bash
btrepl add-slave -s 192.168.1.10:2222
```

## Restricting the master's key

`btrepl add-slave` limits the master's key on the slave to the commands btrepl needs. It rewrites the key's line in the slave's `~/.ssh/authorized_keys` to:

```text
restrict,from="192.168.1.5",command="/usr/local/bin/btrepl ssh-filter" ssh-ed25519 AAAA... root@master
```

- `restrict` turns off port forwarding, agent forwarding and the terminal.
- `from=` accepts the key only from the address the slave sees the master connect from.
- `btrepl ssh-filter` allows only the commands a replication cycle runs: reading `/proc/self/mountinfo`, creating the snapshot directory and setting its mode, listing subvolumes, `btrfs receive --chroot` into the snapshot directory and deleting snapshots in it. It refuses anything else, and runs the allowed commands without a shell.

After the change `add-slave` connects again through the restricted key and runs the commands of a cycle. If that fails, it puts the previous `authorized_keys` back and warns that the key keeps full access. It also only warns, and changes nothing, when btrepl is not installed on the slave or the key is not in `~/.ssh/authorized_keys` (for example with a custom `AuthorizedKeysFile`). Run `add-slave` again after fixing the cause. Use `--no-restrict` to keep the key unrestricted.

To restrict a key by hand, write the line above yourself.

`ssh-filter` reads `btrfs_root` and `snapshot_dir` from the slave's `/etc/btrepl/config.yaml`, or uses the defaults if there is none. They must match the master's config. It accepts exactly the commands of the same btrepl version, so upgrade the master and the slaves together.

`btrfs receive` needs root, so the key still logs in as root on the slave. A non-root user with `sudo` is not supported yet.

!!! note "Upgrading"
    Slaves added by an earlier version keep an unrestricted key. Run `btrepl add-slave -s <host>` once for each of them to restrict it.

## Timeouts

A slave that stops responding must not stall replication to the others:

- The master sends an SSH keepalive every 30 seconds and drops the connection after three unanswered ones.
- Each remote command other than the send must finish within `ssh_command_timeout` (default `2m`).
- A send is aborted when no data reaches the slave for `send_idle_timeout` (default `10m`). Raise it if `btrfs receive` on a slow disk needs longer to finish after the last data.

The cycle then goes on with the next slave.
