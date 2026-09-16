<h1 align="center">aperture-cli</h1>

<p align="center">
  <a href="#supported-agents">Supported agents</a> |
  <a href="#installation">Installation</a> |
  <a href="#usage">Usage</a> |
  <a href="#development">Development</a>
</p>

<p align="center">
  <img src="./assets/aperture-hero.png" alt="Aperture CLI">
</p>

> [!WARNING]
> **This repository is alpha software.** It is under active development and may change significantly without notice.

A CLI launcher for coding agents preconfigured to work with [Aperture](https://aperture.tailscale.com). It manages installation, configuration and environment variables that make using multiple providers and models very easy.

## Supported agents

- [Claude Code](https://docs.anthropic.com/en/docs/claude-code)
- [Gemini CLI](https://github.com/google-gemini/gemini-cli)
- [OpenCode](https://github.com/sst/opencode)
- [Codex](https://github.com/openai/codex)
- [GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-getting-started)
- [Claude Cowork](https://support.claude.com/en/articles/13345190-get-started-with-claude-cowork)

## Installation

```sh
go install github.com/tailscale/aperture-cli/cmd/aperture@latest
```

Or build from source:

```sh
make build
```

## Usage

```sh
aperture
```

On first run, `aperture` will attempt to connect to `http://ai`. If it cannot reach that host, it will prompt you to configure an Aperture endpoint.

### Bridge mode

Bridge mode lets Aperture CLI reach an Aperture endpoint through an embedded Tailscale node. Only the Aperture CLI proxy appears on the tailnet; the rest of the machine does not need the full Tailscale client installed or connected to that tailnet.

This is useful on machines where installing Tailscale is not practical, where Aperture needs to work alongside another VPN, or where you need to switch tailnets while continuing to use a single Aperture instance.

To use bridge mode:

1. Press `c` on the agent menu for `Change connection` (the same screen as `Settings`, then `Aperture Endpoints`).
2. Choose `Add a connection`, then `Bridge`, then an existing bridge or `Add Bridge`.
3. Authorize the bridge. Aperture CLI opens your browser at the Tailscale login link; if it cannot, the link stays on screen to open by hand. No URL is asked for: the bridge looks for Aperture at `http://ai`, the same location a direct connection starts from.
4. Aperture CLI verifies `/v1/models`, makes the endpoint active, and returns to the agent menu.

If your Aperture answers on a different hostname, type it on the connect screen while the default is being tried. That cancels the attempt and connects to what you typed. Esc abandons the attempt and leaves your current endpoint alone.

### Choosing a connection

`Change connection` lists everything this launcher can reach: each saved endpoint, and each bridge that has no endpoint yet, labelled with the tailnet it reaches. That is the screen to use when the launcher connected on its own and you wanted the other bridge.

Selecting a row opens it. From there you can connect to it, change its URL, switch its tailnet, or remove it.

`http://ai` can answer and still be the wrong Aperture, which is what happens when the bridge joins a tailnet that already has a host called `ai`. `Change URL` points the connection somewhere else; it keeps the bridge it is reached through and reconnects.

### Switching tailnets

A bridge is on one tailnet at a time. `Switch tailnet` logs it out, which removes its node from that tailnet, then prints a new login link: open it and pick the tailnet you want. Use a second bridge instead if you want to keep both logins and choose between them.

If verification fails, the endpoint remains configured for retry or editing, and any previous working endpoint remains active.

### Flags

| Flag | Description |
|------|-------------|
| `-version` | Print build version and exit |
| `-debug` | Print environment variables set before launching the agent |

## Development

```sh
make build   # build ./aperture
make test    # run tests
make install # install to $GOPATH/bin
make clean   # remove built binary
```
