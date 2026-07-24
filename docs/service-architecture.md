# Service architecture v2

The kernel treats a `.plg` package as a **service**. A service has one runtime
(`static`, `wasm`, or `grpc`), owns zero or more transports, and binds routes to
those transports. Identity/lifecycle, transport registration, and routing are
separate layers.

## Package files

`info.yaml` contains the stable identity and runtime metadata:

```yaml
Name: docs
Version: 1.0.0
Type: static
ContractVersion: 2
```

`grpc` and `wasm` services also declare `Command`. A static service declares
its resources in `manifest.yaml`:

```yaml
version: 1
transports:
  - id: assets
    type: static
    source: public
routes:
  - id: site
    transport: assets
    http:
      pattern: /
```

Static sources are relative to the extracted `Content` directory. Absolute
paths, traversal, symlinks, and special archive entries are rejected.

## Runtime registration

WASM and gRPC use the same protobuf contract. `Service.Register` performs only
the identity handshake and supplies a `go-plugin` broker stream ID; the service
dials that stream and calls `Host.RegisterTransport`,
`Host.RegisterRoutes`, and their matching unregister operations to manage
resources. Static manifests go through those same kernel registries.

Available transports:

- `static`: serve a file or directory from package content.
- `http`: serialize one HTTP request/response through the service RPC.
- `socket.io`: forward declared Socket.IO events through the service RPC.
- `proxy`: stream HTTP, WebSocket, or an upstream-owned Socket.IO connection to
  an inherited, Unix, or TCP HTTP listener.

Closing a transport is explicit and immediate. A transport cannot be
unregistered while a route still references it. When a service session exits,
the kernel forcibly removes all of its routes and transports so dead bindings
cannot remain.

## gRPC proxy listener

Before a gRPC child starts, the kernel creates:

```text
tmp/plg-xxxxxx/.runtime/       0700
tmp/plg-xxxxxx/.runtime/proxy.sock  0600
```

The listening socket is inherited through `exec.Cmd.ExtraFiles`. Its descriptor
number and metadata are supplied in both environment variables and
`RegisterRequest.listeners`. One listener belongs to the service session and
may serve any number of proxy routes. The kernel closes its duplicate after
`go-plugin` has started the child and removes the runtime directory on unload.

Process startup, the main gRPC handshake, and the reverse Host API stream remain
owned by `github.com/SteelDrEgg/go-plugin` and its `GRPCBroker`; the kernel does
not create a separate TCP callback server.

The current library hook is sufficient but has limitations worth improving
upstream: `ClientConfigOverride` cannot return an error, and it has no
post-`cmd.Start` callback for promptly closing parent copies of inherited
descriptors. The kernel therefore records preparation errors and closes its
copies when `Manager.Load` returns. The wrapper's `GRPCBroker` also does not
expose a per-stream stop handle; today this is acceptable because the broker
closes every Host stream when the service client is unloaded.

## HTTP routing

- A path cannot be shared by different owners, even for different methods.
- An empty method means wildcard and conflicts with every method.
- Static and non-static routes conflict at the same path.
- Exact and prefix routes coexist; the longest matching pattern wins.
- A batch keeps successful registrations. Any failed item marks the service
  `degraded`.

Proxy requests retain end-to-end request headers, including `Cookie` and
`Authorization`. Go's reverse proxy manages hop-by-hop headers and writes
verified forwarding metadata. The kernel always removes caller-supplied
identity headers and injects:

- `X-Arupa-Authenticated`
- `X-Arupa-User`
- one `X-Arupa-Group` value per verified group

The original path, query, and Host header are preserved.

## Kernel package boundaries

The root `internal/service` package is only the composition root and public
facade. Runtime implementation is split by ownership:

```text
service.Manager
├── supervisor  discovery state and start/stop transitions
│   ├── catalog bundle scanning and info.yaml validation
│   └── runner  go-plugin, WASM/gRPC, inherited FD and manifest loading
├── host        KV, Params, messages and authenticated Host capabilities
├── binding     owner-scoped registration transaction coordinator
│   ├── route      conflicts, matching and HTTP dispatch
│   ├── transport  static, RPC and reverse-proxy endpoints
│   └── socketio   namespaces and event dispatch
└── registry    published running-service read model
```

All subsystems depend on the backend-neutral `spec` package. gRPC and WASM
protobuf conversion is isolated under `adapter/grpc` and `adapter/wasm`.
`instance.Instance` is the only shared runtime session: routing sees it through
the narrow `spec.Endpoint` interface, while process handles remain captured by
the runner's cleanup callback.

`binding.Controller` is the only component allowed to coordinate routes and
transports. The two registries do not refer to each other. It serializes
cross-resource mutations, prevents removal of an in-use transport, publishes
the resulting service projection, and performs explicit route-then-transport
cleanup when the supervisor stops an owner.
