# Getting started

These steps assume `btrepl` is already [installed](installation.md) on the master, and the master can SSH to each slave as `root` with a key.

## 1. Initialize the master

```bash
btrepl init-master
```

Creates `/etc/btrepl/config.yaml` with defaults and enables btrfs quota on the root.

## 2. Edit the config

```bash
vim /etc/btrepl/config.yaml
```

```yaml
btrfs_root: /btrfs
ssh_identity: /root/.ssh/id_ed25519
ssh_user: root
snapshot_dir: .snapshots
snapshot_prefix: btrepl_
keep_sender: 428       # snapshots to keep on master
keep_receiver: 10      # snapshots to keep on each slave
subvolumes:
  - "@data"
  - "@postgres"
slaves: []             # managed by add-slave / del-slave
```

## 3. Add a slave

```bash
btrepl add-slave -s 192.168.1.10
```

- Connects to the slave via SSH, creates the snapshot directory.
- Adds the slave to the config.
- Enables and starts `btrepl.timer` (see [Systemd setup](systemd.md)).

## 4. Check it works

```bash
btrepl run                    # run one cycle now
btrepl status -s 192.168.1.10 # snapshot counts and latest snapshot on the slave
```

See [Configuration](configuration.md) for all config keys and [Commands](commands.md) for the full CLI.
