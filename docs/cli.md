# CLI reference

The `cincai` binary manages the gateway, encrypted credentials, and client API keys.

Build and install locally:

```bash
just build          # → bin/cincai
```

Both `-flag` and `--flag` forms are accepted. Run `cincai <command> --help` for flags and defaults.

---

## Global

```bash
cincai help
cincai --help
```

Commands: `init`, `serve`, `catalog`, `credential`, `keys`.

---

## `cincai init`

Scaffold local dev config from tracked examples (idempotent — skips files that already exist).

```bash
cincai init [--dir PATH] [--force]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--dir` | `.` | Project directory to initialize |
| `--force` | `false` | Overwrite existing config files |

Creates `config/cincai.yaml`, `config/providers.yaml`, and `config/cincai.dev.env` (broker key). Works anywhere — a standalone binary falls back to the templates embedded in it; inside a repo checkout the tracked `config/*.example` files are used.

Example:

```bash
cincai init
set -a && source config/cincai.dev.env && set +a
cincai serve --config config/cincai.yaml
```

---

## `cincai serve`

Start the data-plane listener until interrupted (SIGINT/SIGTERM).

```bash
cincai serve [--config PATH]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |

Example:

```bash
set -a && source config/cincai.dev.env && set +a
cincai serve --config config/cincai.yaml
```

Data plane defaults to `127.0.0.1:9420` (loopback). Point your SDK `base_url` at `http://127.0.0.1:9420`.

See [configuration.md](configuration.md) for `cincai.yaml` and env vars.

---

## `cincai catalog`

Tools for `providers.yaml`.

### `validate`

Load the catalog through the gateway normalizer (capabilities → surfaces, wire-translate) and check every model modality resolves.

```bash
cincai catalog validate [--config PATH] [--catalog PATH]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config (uses `serve.catalog`) |
| `--catalog` | — | Path to `providers.yaml` (overrides config) |

Example:

```bash
cincai catalog validate --config config/cincai.yaml
cincai catalog validate --catalog config/providers.yaml
```

Exit 0 prints `catalog ok: …`; non-zero on load or routing errors.

---

## `cincai credential`

Manage secrets in the encrypted broker (`broker.db`). Requires `CINCAI_BROKER_KEY` — see [credential.md](credential.md) and [configuration.md](configuration.md).

```bash
cincai credential <subcommand> [flags]
```

### `login` — OAuth sign-in

```bash
cincai credential login PROFILE [flags]
cincai credential login --list
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--flow` | `auto` | `auto`, `browser`, `device`, or `manual` |
| `--callback-listen` | `127.0.0.1:0` | Loopback address for browser OAuth callback |
| `--list` | — | List vendor OAuth profiles and exit |

Vendor profiles in the stock binary: `xai-oauth` (Grok). Other providers use `credential import` with an API key, or a custom OAuth pack linked into your binary.

Remote servers: see [oauth.md](oauth.md) (SSH port-forward or `--flow manual`).

### `import` — store an API key

```bash
cincai credential import PROFILE --api-key KEY [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--api-key` | *(required)* | API key value |

`PROFILE` must match `credential_profile` in `providers.yaml` (e.g. `deepseek-api`).

### `list` — list broker credentials

```bash
cincai credential list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--all` | `false` | Include disabled credentials |

Output is JSON (id, profile, status, kind).

### `refresh` — refresh OAuth tokens

```bash
cincai credential refresh PROFILE [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |

`cincai serve` also refreshes tokens automatically while running.

### `enable` / `disable`

```bash
cincai credential enable ID [flags]
cincai credential disable ID [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--cause` | `manual` | Reason recorded when disabling (`disable` only) |

`ID` is the numeric credential id from `cincai credential list`.

---

## `cincai keys`

Mint and manage gateway **client** keys (what SDKs send as `Authorization: Bearer …`). Distinct from upstream credentials in the broker.

```bash
cincai keys <subcommand> [flags]
```

### `create`

```bash
cincai keys create [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--name` | `default` | Key display name |
| `--static` | `true` | Create a static (non-expiring) key |
| `--ttl` | `720h` | TTL when `--static=false` |
| `--scopes` | `*` | Comma-separated scopes: `model:ID`, `wire:ID`, or `*` |

Prints `key=…` once — store it; it is not shown again.

Scope examples:

- `*` — full access
- `wire:openai-chat-completions` — chat completions only
- `model:example-model` — one catalog model id

### `list`

```bash
cincai keys list [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |

### `set-scopes`

```bash
cincai keys set-scopes ID --scopes model:demo,wire:openai-chat-completions [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--scopes` | *(required)* | New scopes (replaces all existing scopes) |

Updates scopes **in place** — the secret (`sk-dg-…`) is unchanged. Rejects unknown or revoked key IDs. Takes effect on the next request (no gateway restart).

### `set-name`

```bash
cincai keys set-name ID --name friend-alice [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--name` | *(required)* | New display name (`principal_id` in usage logs) |

Renames **in place** — the secret is unchanged. Rejects empty names and unknown/revoked IDs.

### `set-limits`

```bash
cincai keys set-limits ID --max-tokens 500000 [--window 24h]
cincai keys set-limits ID --max-tokens 0          # clear budget
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |
| `--max-tokens` | *(required)* | Rolling budget of **input+output** tokens; `0` clears |
| `--window` | `24h` | Rolling window (`time.ParseDuration`, e.g. `24h`, `168h`) |

When set, the data plane **rejects** further authenticated requests with **HTTP 429**
(`usage_limit_exceeded`) once measured tokens in the window reach the cap. Usage is
counted after each successful response (slight overshoot on the last call is possible).
`keys list` shows `budget_max_tokens`, `budget_window`, and current `budget_used`.

### `revoke`

```bash
cincai keys revoke ID [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config/cincai.yaml` | Path to gateway config file |

`ID` from `cincai keys list`.

---

## See also

- [configuration.md](configuration.md) — `cincai.yaml`, env vars, adapters
- [credential.md](credential.md) — broker, profiles, API keys vs OAuth
- [oauth.md](oauth.md) — remote OAuth login