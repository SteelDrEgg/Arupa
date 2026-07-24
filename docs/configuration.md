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

`Update` accepts one or more ordered path operations. At the start of the
transaction it reads and validates the latest `config.toml`, uses that
configuration as its base, and applies the operations to both a shallow
copy-on-write candidate and a layout-preserving TOML document. It verifies that
both results are equivalent, replaces `config.toml` with mode `0600`, and then
publishes the candidate in memory. A validation or persistence failure leaves
the effective in-memory configuration unchanged. All reloads and updates are
serialized by `conf`.

Consequently, a successful `Update` also makes other valid edits already
present on disk effective, even if `Reload` was not called first. An invalid
disk document causes `Update` to fail without changing the current
configuration.

Update paths use case-sensitive JSON Pointer syntax. Fixed field names are
declared by `conf` constants; dynamic map keys are escaped as JSON Pointer
segments. The current rules are:

- `Set` rejects a nil value.
- `Remove` of a missing path succeeds without changing configuration.
- Missing map nodes are created by `Set`.
- Arrays are replaced as a whole.
- Operations in one `Update` are applied in the supplied order.

Only the bytes expressing an updated path are rewritten. Comments, whitespace,
line endings, and ordering outside the updated value are retained. Replacing or
removing a complete table also removes comments attached to that table and its
contents.

Cross-process coordination with a future standalone configuration CLI is
intentionally left as a TODO. Without cooperative locking, an external write
that races between Update's read and atomic replacement remains last-writer
wins.

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
