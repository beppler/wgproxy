# wgproxy

wgproxy is a configurable [HTTP/HTTPS](https://en.wikipedia.org/wiki/Proxy_server) proxy server that forwards the outgoing connections through a [WireGuard](https://www.wireguard.com/) tunnel and provide a host for [Proxy Auto-Configuration](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/Proxy_servers_and_tunneling/Proxy_Auto-Configuration_PAC_file) file that simplify the proxy usage.

## How it works

The proxy handles:

* **CONNECT** requests by opening a TCP tunnel to the destination host through the WireGuard interface.
* **Absolute URI** requests (plain HTTP proxying) by forwarding them through a transport backed by the WireGuard dialer.
* A **`/proxy.pac`** endpoint which serves a [Proxy Auto-Configuration](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/Proxy_servers_and_tunneling/Proxy_Auto-Configuration_PAC_file) file, so browsers can automatically decide when to use the proxy (local and private network addresses bypass it).

Any other request receives a `405 Method Not Allowed` response.

Each request is tagged with a unique request id ([ULID](https://github.com/oklog/ulid)), included in the structured logs, and every request is logged with its method, URI, remote address, status code, and duration.

## Usage

Pre-built binaries for Linux, macOS, and Windows are available on the [releases page](https://github.com/beppler/wgproxy/releases). Download the archive for your platform, extract it, and run the `wgproxy` executable.

Alternatively, run the server with Go:

```sh
# run it from GitHub
go run github.com/beppler/wgproxy/cmd/wgproxy@latest

# install it from from GitHub
go install github.com/beppler/wgproxy/cmd/wgproxy@latest
wgproxy

# to run it from local source
go run ./cmd/wgproxy
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

## Release

Releases are published automatically by a [GitHub workflow](.github/workflows/release.yml) whenever a tag starting with `v` is pushed. The workflow builds the archives for every platform with [`build.sh`](build.sh), generates a `SHA256SUMS` file, and uploads them to a GitHub release named after the tag.

The release notes are taken from the tag message, so the tag must be annotated or signed:

```sh
# opens an editor to write the release notes (Markdown is supported)
git tag -s v1.0.0

# or provide the release notes inline
git tag -s v1.0.0 -m "Release notes"

git push origin v1.0.0
```

Tags containing a hyphen (for example `v1.1.0-rc.1`) are published as pre-releases.

To build the archives locally without releasing, run `bash build.sh`; they are written to the `dist/` directory.

## License

[MIT](LICENSE)
