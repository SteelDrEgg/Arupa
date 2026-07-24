# Configuration model

`config.toml` is the persistent representation of configuration. The effective
configuration is the validated state currently held by `internal/conf`.
Runtime code reads configuration through `conf` queries and does not retain a
second long-lived configuration tree.

Relative paths are resolved against the process working directory.

## Reload and update

`Reload` reads the complete `config.toml`, rejects unknown or incorrectly cased
fields, validates the candidate, and publishes it in memory only when the whole
file is valid. An empty file is valid and selects the hard-coded defaults.

`Update` accepts one or more ordered path operations. It builds a shallow
copy-on-write candidate, validates it, replaces `config.toml` with mode `0600`,
and then publishes the candidate in memory. A persistence failure leaves the
effective in-memory configuration unchanged. All reloads and updates are
serialized by `conf`.

Update paths use case-sensitive JSON Pointer syntax. Fixed field names are
declared by `conf` constants; dynamic map keys are escaped as JSON Pointer
segments. The current rules are:

- `Set` rejects a nil value.
- `Remove` of a missing path succeeds without changing configuration.
- Missing map nodes are created by `Set`.
- Arrays are replaced as a whole.
- Operations in one `Update` are applied in the supplied order.

The logical update is copy-on-write, but the current persistence implementation
rewrites the complete TOML document. Preserving comments and applying
incremental file edits are future work. Cross-process coordination with a
future standalone configuration CLI is also intentionally left as a TODO.

## Defaults and services

Defaults are hard-coded and are consulted when the current configuration does
not set a field. Defaults are documented behavior and are not written to
`config.toml`.

To `conf`, `Services.default` is an ordinary service entry. The Service Manager
interprets it as the base for a named service. Defining Params on it is allowed
but discouraged.

For service inheritance:

- An unset `Allow` inherits the default service value. An explicitly empty
  `Allow = []` overrides it and leaves the service open.
- Empty or whitespace-only `RunAsUser` and `Checksum` do not override inherited
  values.
- Params are merged by key and the named service wins.

## When changes take effect

| Configuration | Effective boundary |
| --- | --- |
| `Users`, `Groups`, `Route`, `Pages` | The next authentication or HTTP request |
| `Services.<name>.Allow` | The next request to that service, including already running services |
| `ServiceDir` | The next service scan; list, start, and restart scan on demand |
| `ServiceTempDir` | The next service load; a running service keeps its current extraction directory |
| Service `Restart`, `RunAsUser`, `Checksum` | The next start or explicit restart |
| Service `Params` | The next Params query and the next start; Params already supplied during registration are not pushed into a running service |
| `Listen`, `TLS`, `Log` | Kernel startup; changing them requires restarting the kernel |

Existing authentication sessions currently remain valid until logout or
expiry after user configuration changes. The desired invalidation behavior is
intentionally left as a TODO.
