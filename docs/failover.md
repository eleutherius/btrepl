# Failover

## Promoting a slave

On the slave node:

```bash
# Install btrepl and copy the master config, then:
btrepl standalone
```

This stops the timer, deletes the existing writable subvolume, and replaces it with a writable snapshot of the latest received replica. The node is now fully independent.
