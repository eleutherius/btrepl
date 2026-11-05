# Systemd setup

## Daemon mode

```bash
cp deploy/btrepl.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now btrepl.service
```

The unit creates `/run/btrepl` (mode `0700`) for the API socket. To expose the API over TCP with mutual TLS, override `ExecStart` as shown in [gRPC API](grpc.md#over-tcp-with-mutual-tls).

## Timer mode

`btrepl add-slave` and `btrepl del-slave` call `systemctl enable/start/stop/disable` automatically. Unit files are in [`deploy/`](https://github.com/eleutherius/btrepl/tree/main/src/btrepl/deploy) — install them first:

Reload and the CLI will handle the rest:

```bash
cp deploy/btrepl.{service,timer} /etc/systemd/system/
systemctl daemon-reload
btrepl add-slave -s <IP>   # enables + starts the timer
```
