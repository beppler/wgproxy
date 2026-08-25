# wgproxy

wgproxy is a configurable [HTTP/HTTPS](https://en.wikipedia.org/wiki/Proxy_server) proxy server that forwards the outgoing connections through a [WireGuard](https://www.wireguard.com/) tunnel.

## How it works

The proxy handles:

* **CONNECT** requests by opening a TCP tunnel to the destination host through the WireGuard interface.
* **Absolute URI** requests (plain HTTP proxying) by forwarding them through a transport backed by the WireGuard dialer.
* A **`/proxy.pac`** endpoint which serves a [Proxy Auto-Config](https://en.wikipedia.org/wiki/Proxy_auto-config) file, so browsers can automatically decide when to use the proxy (local and private network addresses bypass it).

Any other request receives a `405 Method Not Allowed` response.

Each request is tagged with a unique request id ([ULID](https://github.com/oklog/ulid)), included in the structured logs, and every request is logged with its method, URI, remote address, status code, and duration.

## Usage

Run the server:

```sh
# to run it from local source
go run ./cmd/wgproxy

# to run it from GitHub
go install github.com/beppler/wgproxy/cmd/wgproxy
wgproxy
```

Available flags:

| Flag             | Default          | Description                                |
| ---------------- | ---------------- | ------------------------------------------ |
| `-address`       | `localhost:1357` | Address to listen on                       |
| `-configuration` | `wg0.conf`       | Path to the WireGuard configuration file   |
| `-proxy-pac`     | `proxy.pac`      | Path to the `proxy.pac` file               |

Example:

```sh
wgproxy -address localhost:1357 -configuration /etc/wireguard/wg0.conf -proxy-pac /etc/wgproxy/proxy.pac
```

The server shuts down gracefully on `SIGINT`/`SIGTERM`.

## Configuration

An example WireGuard configuration is provided in [`sample-wg0.conf`](sample-wg0.conf), and an example proxy auto-config file in [`sample-proxy.pac`](sample-proxy.pac).

## Dependencies

This project depends on code from:

* [httpgrace](https://github.com/enrichman/httpgrace) to manage HTTP server graceful shutdown.
* [wiredialer](https://github.com/botanica-consulting/wiredialer) to interact with WireGuard protocol.
* [ulid](https://github.com/oklog/ulid) to generate request ids.

## License

[MIT](LICENSE)
