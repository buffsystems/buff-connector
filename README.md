# Buff Connector Agent

A small, self-contained agent you run **inside your own network** to connect a
firewalled or self-hosted git provider (GitLab, GitHub Enterprise, Gitea/Forgejo,
Bitbucket Server) — or a public SaaS provider (github.com, gitlab.com,
bitbucket.org) — to the Buff Development platform, **without exposing your git
server** and **without handing us your git credentials**.

## How it works (in one picture)

```
your git ◀─LAN─▶ CONNECTOR ──outbound──▶ Buff relay ◀──long-poll── Buff backend
(behind          (this agent,            (middleware)
 your firewall)   dials OUT only)
```

- The agent only ever **dials out** to the Buff relay — nothing connects *in* to
  your network or to Buff.
- It touches your git **only when a task arrives** (import a repo, publish a pull
  request). It never polls on its own.
- Your git **token stays in this agent**, in your network. It is never sent to Buff.

## Two commands

- `connector pair --config <path> --code <pairing-code>` — one-time setup: pair
  this agent with your organization using a code you generate in the Buff app.
- `connector run --config <path>` — run the agent (as a service / container).

See the configuration reference and full setup/uninstall guide on the Buff site.

## Security model

- **No secrets in the distributed binary/image.** The agent generates its own
  keypair on first run; pairing injects only a short-lived one-time code.
- **Least privilege.** A paired agent is confined to a single organization and is
  revocable at any time from the Buff app.
- Distribution artifacts are signed (cosign); verify before running.

## Development

Requires Go 1.22+.

```
make test    # unit tests (TDD)
make vet
make build   # local binary
```
