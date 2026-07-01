package netx

import (
	"fmt"
	"github.com/zishang520/socket.io/servers/engine/v3"
	"github.com/zishang520/socket.io/servers/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"
	"net/http"
	"sync"
)

// Socket represents a wrapper around the Socket.IO server
type Socket struct {
	sock       *socket.Server
	Namespaces map[string]Namespace
}

// Initialize configures and creates the Socket.IO server
func (self *Socket) Initialize() {
	opts := socket.DefaultServerOptions()
	opts.SetPath("/socket.io")
	opts.SetTransports(types.NewSet(
		engine.Polling,   // HTTP long-polling transport
		engine.WebSocket, // WebSocket transport for real-time communication
	))
	opts.SetMaxHttpBufferSize(1e7) // 10MB
	self.sock = socket.NewServer(nil, opts)
	self.Namespaces = make(map[string]Namespace)
}

// AddNamespace creates a new Socket.IO namespace and adds it to the server.
// It is idempotent: calling it again for an existing namespace is a no-op.
func (self *Socket) AddNamespace(name string) {
	if _, ok := self.Namespaces[name]; ok {
		return
	}
	namespace := Namespace{namespace: self.sock.Of(name, nil)}
	namespace.Initialize()
	self.Namespaces[name] = namespace
}

// HasNamespace reports whether the named namespace has been added.
func (self *Socket) HasNamespace(name string) bool {
	_, ok := self.Namespaces[name]
	return ok
}

// GetNamespace returns the desired namespace
func (self *Socket) GetNamespace(name string) Namespace {
	return self.Namespaces[name]
}

// Handler returns an HTTP handler for the Socket.IO server
func (self *Socket) Handler() http.Handler {
	return self.sock.ServeHandler(nil)
}

// Namespace represents a Socket.IO namespace with custom event handling
type Namespace struct {
	namespace socket.Namespace
	events    map[string]func(client *socket.Socket, data ...any)
	//middileWare []func(client *socket.Socket, next func())
}

// Raw returns the underlying Socket.IO namespace for advanced use such as the
// plugin Socket.IO bridge. It uses a value receiver so it can be called on the
// namespaces returned by Socket.GetNamespace.
func (self Namespace) Raw() socket.Namespace {
	return self.namespace
}

// Initialize sets up the namespace with default event handlers
func (self *Namespace) Initialize() {
	self.events = map[string]func(*socket.Socket, ...any){
		"disconnect": func(client *socket.Socket, reason ...any) {},
	}
}

// AddEvent registers a custom event handler for the namespace
func (self *Namespace) AddEvent(event string, f func(*socket.Socket, ...any)) {
	self.events[event] = f
}

// RegisterEvents activates all the event handlers for new client connections
func (self *Namespace) RegisterEvents() {
	self.namespace.On("connection", func(clients ...any) {
		client := clients[0].(*socket.Socket)
		for event, f := range self.events {
			client.On(event, func(data ...any) { f(client, data...) })
		}
	})
}

// AddMiddleware adds a middleware to the namespace
func (self *Namespace) AddMiddleware(f func(client *socket.Socket, next func(*socket.ExtendedError))) {
	self.namespace.Use(f)
}

// OnConnection registers a connection handler for this namespace.
func (self Namespace) OnConnection(f func(client *socket.Socket)) {
	self.namespace.On("connection", func(clients ...any) {
		if len(clients) == 0 {
			return
		}
		client, ok := clients[0].(*socket.Socket)
		if !ok {
			return
		}
		f(client)
	})
}

// Emit sends an event to all sockets in this namespace.
func (self Namespace) Emit(event string, args ...any) error {
	return self.namespace.Emit(event, args...)
}

// EmitTo sends an event to a specific room/socket target.
func (self Namespace) EmitTo(target, event string, args ...any) error {
	return self.namespace.To(socket.Room(target)).Emit(event, args...)
}

// DisconnectSockets disconnects all sockets currently attached to this namespace.
// When status is false, the underlying Engine.IO connection can remain alive for
// other namespaces.
func (self Namespace) DisconnectSockets(status bool) {
	if self.namespace == nil {
		return
	}
	self.namespace.DisconnectSockets(status)
}

// GlobalServer holds the singleton Socket.IO server instance
var (
	globalServer *Socket
	once         sync.Once
)

// GetGlobalServer returns the singleton Socket.IO server instance
func GetGlobalServer() *Socket {
	once.Do(func() {
		globalServer = new(Socket)
		globalServer.Initialize()
	})
	return globalServer
}

// SetupGlobalServer initializes the global Socket.IO server with all required namespaces
// This should be called once during application startup
func SetupGlobalServer() *Socket {
	server := GetGlobalServer()

	// Add SSH namespace
	server.AddNamespace("/ssh")

	// Add Dashboard namespace
	server.AddNamespace("/dashboard")

	return server
}

// GetHandler returns the HTTP handler for the global Socket.IO server
func GetHandler() http.Handler {
	return GetGlobalServer().Handler()
}

// Test function, ignore this
func Start(addr string) error {
	server := new(Socket)
	server.Initialize()
	server.AddNamespace("/ttt")

	defaultNamespace := server.Namespaces["/ttt"]

	defaultNamespace.AddEvent("message", func(client *socket.Socket, data ...any) {
		client.Emit("message", data...)
	})
	defaultNamespace.RegisterEvents()

	defaultNamespace.AddMiddleware(func(client *socket.Socket, next func(*socket.ExtendedError)) {
		fmt.Println(client.Handshake().Auth)
		next(nil)
	})
	http.Handle("/socket.io/", server.Handler())

	fmt.Println("Socket.IO服务器启动在: http://localhost:8080")
	fmt.Println("访问测试页面: http://DrEggs-Mac-Pro-3.local:8080")
	//StartFrontend()

	return http.ListenAndServe(":8080", nil)
}
