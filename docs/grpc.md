# gRPC API

`btrepl serve` starts a long-running daemon that exposes a gRPC server and runs the replication loop internally based on `interval` from the config. No systemd timer needed.

The API can change where a node replicates its data, so it is never open to the network without authentication:

- By default the daemon listens on the Unix socket `/run/btrepl/btrepl.sock`. The socket has mode `0600` in a `0700` directory, so only root on the node can use it.
- It listens on TCP only with mutual TLS: clients must present a certificate signed by a CA you choose. Without the TLS files, `serve` refuses to start on a TCP address.

## Connecting

### On the node

As root, connect to `unix:/run/btrepl/btrepl.sock`.

### From another machine over SSH

If you can log in to the node as root, forward a local port to the socket:

```bash
ssh -N -L 127.0.0.1:50051:/run/btrepl/btrepl.sock root@master
```

Then connect to `127.0.0.1:50051`. SSH authenticates and encrypts the connection, so this needs no TLS setup.

### Over TCP with mutual TLS

Give `serve` a TCP address, the server certificate and key, and the CA that signs the client certificates:

```bash
systemctl edit btrepl.service
```

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/btrepl serve --addr 0.0.0.0:50051 \
    --tls-cert /etc/btrepl/tls/server.crt \
    --tls-key /etc/btrepl/tls/server.key \
    --tls-client-ca /etc/btrepl/tls/clients-ca.crt
```

The server certificate must name the address clients connect to (a `subjectAltName` with the host name or IP). Every client with a certificate from `clients-ca.crt` gets full access to the API, so use a dedicated CA and keep the server key readable only by root. TLS 1.3 is required.

!!! note "Upgrading from 0.1"
    Earlier versions listened on `:50051` without authentication. After the upgrade the daemon listens on the socket only. Clients that connected to port 50051 need an SSH tunnel to the socket or mutual TLS.

## Proto

```protobuf
service Btrepl {
  rpc Run(RunRequest)             returns (RunResponse);
  rpc Status(StatusRequest)       returns (StatusResponse);
  rpc AddSlave(SlaveRequest)      returns (SlaveResponse);
  rpc DelSlave(SlaveRequest)      returns (SlaveResponse);
  rpc WatchLogs(WatchLogsRequest) returns (stream LogEntry);
}
```

Full definition: [`api/btrepl.proto`](https://github.com/eleutherius/btrepl/blob/main/src/btrepl/api/btrepl.proto)

## Python client

The [`btrepl` package](https://github.com/eleutherius/btrepl/tree/main/src/pybtrepl) wraps the generated stubs:

```python
from btrepl import BtreplClient

# on the node, as root
with BtreplClient("unix:/run/btrepl/btrepl.sock") as c:
    c.run()                       # trigger replication now
    print(c.status())

# through an SSH tunnel to the socket
with BtreplClient("127.0.0.1:50051") as c:
    c.add_slave("192.168.139.196")
    for entry in c.watch_logs():  # stream logs in real time
        print(f"[{entry.level}] {entry.message}")

# over TCP with mutual TLS
read = lambda p: open(p, "rb").read()
with BtreplClient("master.example:50051",
                  root_certificates=read("server-ca.crt"),
                  certificate_chain=read("client.crt"),
                  private_key=read("client.key")) as c:
    print(c.status())
```
