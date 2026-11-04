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

By default the master's key can run any command as root on the slave. To limit it to the commands btrepl needs, make `btrepl ssh-filter` the forced command for that key in the slave's `/root/.ssh/authorized_keys`:

```text
restrict,from="192.168.1.5",command="/usr/local/bin/btrepl ssh-filter" ssh-ed25519 AAAA... root@master
```

- `restrict` turns off port forwarding, agent forwarding and the terminal.
- `from=` accepts the key only from the master's address.
- `btrepl ssh-filter` allows only the commands a replication cycle runs: reading `/proc/self/mountinfo`, creating the snapshot directory, listing subvolumes, `btrfs receive` into the snapshot directory and deleting snapshots in it. It refuses anything else, and runs the allowed commands without a shell.

`ssh-filter` reads `btrfs_root` and `snapshot_dir` from the slave's `/etc/btrepl/config.yaml`, or uses the defaults if there is none. They must match the master's config.

`btrfs receive` needs root, so the key still logs in as root on the slave. A non-root user with `sudo` is not supported yet.
