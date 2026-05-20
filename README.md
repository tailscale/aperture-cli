<p align="center">
  <img src="./assets/aperture-logo.png" alt="Aperture" width="320">
</p>

<h1 align="center">aperture-cli</h1>

<p align="center">
  <a href="#supported-agents">Supported agents</a> |
  <a href="#installation">Installation</a> |
  <a href="#usage">Usage</a> |
  <a href="#development">Development</a>
</p>

> [!WARNING]
> **This repository is alpha software.** It is under active development and may change significantly without notice.

A CLI launcher for coding agents preconfigured to work with [Aperture](https://aperture.tailscale.com). It manages installation, configuration and environment variables that make using multiple providers and models very easy.

<video src="./assets/aperture-cli-demo.mp4" controls width="100%"></video>

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
