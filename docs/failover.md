# Failover

## Promoting a slave

On the slave node:

```bash
# Install btrepl and copy the master config, then:
btrepl standalone
```

This stops the timer and the daemon, and replaces each subvolume with a writable snapshot of its latest complete received snapshot. Snapshots left by an interrupted receive are skipped. The writable snapshot is created next to the existing subvolume first and then swapped in, so if a step fails the existing subvolume stays in place. The node is now fully independent.
