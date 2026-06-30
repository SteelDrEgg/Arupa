package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	hcplugin "github.com/hashicorp/go-plugin"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"minimalpanel/internal/sshc"
	panel "minimalpanel/pluginsdk/grpc/proto"
)

const pluginName = "default_grpc"

var handshake = hcplugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "MINIMALPANEL_PLUGIN",
	MagicCookieValue: "minimalpanel",
}

func main() {
	hcplugin.Serve(&hcplugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]hcplugin.Plugin{
			pluginName: &sshPlugin{},
		},
		GRPCServer: hcplugin.DefaultGRPCServer,
	})
}

type sshPlugin struct {
	hcplugin.NetRPCUnsupportedPlugin
}

func (p *sshPlugin) GRPCServer(_ *hcplugin.GRPCBroker, s *grpc.Server) error {
	panel.RegisterPluginServer(s, newSSHServer())
	return nil
}

func (p *sshPlugin) GRPCClient(context.Context, *hcplugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, fmt.Errorf("plugin process does not use GRPCClient")
}

type sshServer struct {
	panel.UnimplementedPluginServer

	mu            sync.RWMutex
	host          panel.HostClient
	hostConn      *grpc.ClientConn
	sshConfigPath string
	sessions      map[string]*sshSession
}

type sshSession struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser

	mu     sync.Mutex
	active bool
}

type connectRequest struct {
	Host       string `json:"host"`
	Port       string `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"privateKey"`
	Passphrase string `json:"passphrase"`
}

type resizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func newSSHServer() *sshServer {
	return &sshServer{
		sessions: make(map[string]*sshSession),
	}
}

func (s *sshServer) Register(ctx context.Context, req *panel.RegisterRequest) (*panel.RegisterReply, error) {
	if req.GetHostCallbackAddr() != "" {
		conn, err := grpc.DialContext(
			ctx,
			req.GetHostCallbackAddr(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithUnaryInterceptor(func(ctx context.Context, method string, in, out any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				ctx = metadata.AppendToOutgoingContext(ctx, "x-panel-token", req.GetHostCallbackToken())
				return invoker(ctx, method, in, out, cc, opts...)
			}),
		)
		if err != nil {
			return nil, fmt.Errorf("dial host callback: %w", err)
		}
		s.hostConn = conn
		s.host = panel.NewHostClient(conn)
	}

	s.sshConfigPath = req.GetParams()["ssh_config_path"]
	s.log(ctx, "info", "ssh plugin registered")

	return &panel.RegisterReply{
		Name:    "ssh",
		Version: "0.1.0",
		StaticMounts: []*panel.StaticMount{
			{
				Prefix:    "/pages/terminal.html",
				Directory: "$PLUGIN_ROOT/pages/terminal.html",
				//Directory: "coreplugins/ssh/pages/terminal.html",
				Protected: true,
			},
			{
				Prefix:    "/assets/terminal/",
				Directory: "$PLUGIN_ROOT/assets/terminal",
				Protected: true,
			},
		},
		SocketNamespaces: []*panel.SocketNamespace{
			{
				Name:      "/ssh",
				Events:    []string{"connect_ssh", "terminal_input", "resize", "disconnect"},
				Protected: true,
			},
		},
	}, nil
}

func (s *sshServer) HandleHTTP(context.Context, *panel.HTTPRequest) (*panel.HTTPResponse, error) {
	return &panel.HTTPResponse{Status: http.StatusNotFound}, nil
}

func (s *sshServer) HandleSocketEvent(ctx context.Context, ev *panel.SocketEvent) (*panel.SocketEventReply, error) {
	switch ev.GetEvent() {
	case "connect_ssh":
		return &panel.SocketEventReply{}, s.connectSSH(ctx, ev)
	case "terminal_input":
		return &panel.SocketEventReply{}, s.writeInput(ctx, ev)
	case "resize":
		return &panel.SocketEventReply{}, s.resize(ctx, ev)
	case "disconnect":
		s.cleanup(ev.GetSocketId())
		return &panel.SocketEventReply{}, nil
	default:
		return &panel.SocketEventReply{}, nil
	}
}

func (s *sshServer) connectSSH(ctx context.Context, ev *panel.SocketEvent) error {
	var req connectRequest
	if err := decodeFirstArg(ev.GetPayload(), &req); err != nil {
		return s.emitError(ctx, ev.GetSocketId(), "Invalid connection data: "+err.Error())
	}

	req.Host = strings.TrimSpace(req.Host)
	req.Port = strings.TrimSpace(req.Port)
	req.Username = strings.TrimSpace(req.Username)
	req.PrivateKey = expandHome(strings.TrimSpace(req.PrivateKey))
	if req.Port == "" {
		req.Port = "22"
	}
	if req.Host == "" || req.Username == "" {
		return s.emitError(ctx, ev.GetSocketId(), "Host and username are required")
	}

	hostConfig := s.resolveHostConfig(req)
	authMethods, err := s.authMethods(req, hostConfig)
	if err != nil {
		return s.emitError(ctx, ev.GetSocketId(), err.Error())
	}

	sshClient, err := sshc.Connect(hostConfig, authMethods)
	if err != nil {
		return s.emitError(ctx, ev.GetSocketId(), "SSH connection failed: "+err.Error())
	}

	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		return s.emitError(ctx, ev.GetSocketId(), "Failed to create SSH session: "+err.Error())
	}

	stdin, stdout, err := sshc.SetupTerminal(session, 24, 80)
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return s.emitError(ctx, ev.GetSocketId(), "Failed to setup terminal: "+err.Error())
	}

	if err := session.Shell(); err != nil {
		_ = stdin.Close()
		_ = session.Close()
		_ = sshClient.Close()
		return s.emitError(ctx, ev.GetSocketId(), "Failed to start shell: "+err.Error())
	}

	sshSess := &sshSession{
		client:  sshClient,
		session: session,
		stdin:   stdin,
		active:  true,
	}

	s.mu.Lock()
	if prev := s.sessions[ev.GetSocketId()]; prev != nil {
		prev.close()
	}
	s.sessions[ev.GetSocketId()] = sshSess
	s.mu.Unlock()

	go s.pipeOutput(ev.GetSocketId(), stdout, sshSess)

	return s.emit(ctx, ev.GetSocketId(), "ssh_connected", map[string]any{
		"host": req.Host,
		"port": req.Port,
		"user": req.Username,
	})
}

func (s *sshServer) resolveHostConfig(req connectRequest) *sshc.Host {
	if !strings.Contains(req.Host, ".") && req.Host != "localhost" {
		if cfg, err := sshc.LoadConfig(req.Host, expandHome(s.sshConfigPath)); err == nil {
			if req.Username != "" {
				cfg.User = req.Username
			}
			if req.Port != "" {
				cfg.Port = req.Port
			}
			return cfg
		}
	}

	return &sshc.Host{
		User:     req.Username,
		Host:     req.Host,
		Hostname: req.Host,
		Port:     req.Port,
		Timeout:  30 * time.Second,
	}
}

func (s *sshServer) authMethods(req connectRequest, hostConfig *sshc.Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if req.Password != "" {
		methods = append(methods, ssh.Password(req.Password))
	}

	keyPaths := make([]string, 0, 4)
	if req.PrivateKey != "" {
		keyPaths = append(keyPaths, req.PrivateKey)
	} else if hostConfig.IdentityFile != "" {
		keyPaths = append(keyPaths, hostConfig.IdentityFile)
	}
	if len(keyPaths) == 0 && req.Password == "" {
		keyPaths = append(keyPaths, "$HOME/.ssh/id_rsa", "$HOME/.ssh/id_ed25519", "$HOME/.ssh/id_ecdsa")
	}

	for _, keyPath := range keyPaths {
		auth, err := sshc.LoadAuth("", []*sshc.Identity{{
			KeyPath:    expandHome(keyPath),
			Passphrase: req.Passphrase,
		}})
		if err == nil {
			methods = append(methods, auth...)
			if req.PrivateKey == "" {
				break
			}
		}
	}
	if len(methods) == 0 && req.Password == "" && req.PrivateKey == "" {
		for _, keyPath := range []string{"$HOME/.ssh/id_rsa", "$HOME/.ssh/id_ed25519", "$HOME/.ssh/id_ecdsa"} {
			auth, err := sshc.LoadAuth("", []*sshc.Identity{{
				KeyPath:    keyPath,
				Passphrase: req.Passphrase,
			}})
			if err == nil {
				methods = append(methods, auth...)
				break
			}
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no valid authentication method provided. Please provide either a password or a valid private key")
	}
	return methods, nil
}

func (s *sshServer) writeInput(ctx context.Context, ev *panel.SocketEvent) error {
	var input string
	if err := decodeFirstArg(ev.GetPayload(), &input); err != nil {
		return nil
	}

	sshSess := s.session(ev.GetSocketId())
	if sshSess == nil || !sshSess.isActive() {
		return s.emitError(ctx, ev.GetSocketId(), "No active SSH session")
	}

	sshSess.mu.Lock()
	defer sshSess.mu.Unlock()
	if sshSess.stdin == nil {
		return nil
	}
	if _, err := sshSess.stdin.Write([]byte(input)); err != nil {
		return s.emitError(ctx, ev.GetSocketId(), "Failed to send input")
	}
	return nil
}

func (s *sshServer) resize(_ context.Context, ev *panel.SocketEvent) error {
	var req resizeRequest
	if err := decodeFirstArg(ev.GetPayload(), &req); err != nil {
		return nil
	}
	if req.Cols <= 0 || req.Rows <= 0 {
		return nil
	}

	sshSess := s.session(ev.GetSocketId())
	if sshSess == nil || !sshSess.isActive() {
		return nil
	}

	sshSess.mu.Lock()
	defer sshSess.mu.Unlock()
	if sshSess.session != nil {
		return sshSess.session.WindowChange(req.Rows, req.Cols)
	}
	return nil
}

func (s *sshServer) pipeOutput(socketID string, stdout io.Reader, sshSess *sshSession) {
	reader := bufio.NewReader(stdout)
	buf := make([]byte, 1024)
	for sshSess.isActive() {
		n, err := reader.Read(buf)
		if n > 0 {
			_ = s.emit(context.Background(), socketID, "terminal_output", string(buf[:n]))
		}
		if err != nil {
			if err != io.EOF {
				_ = s.emitError(context.Background(), socketID, "SSH session closed: "+err.Error())
			} else {
				_ = s.emit(context.Background(), socketID, "ssh_disconnected", "SSH session closed")
			}
			s.cleanup(socketID)
			return
		}
	}
}

func (s *sshServer) session(socketID string) *sshSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[socketID]
}

func (s *sshServer) cleanup(socketID string) {
	s.mu.Lock()
	sshSess := s.sessions[socketID]
	delete(s.sessions, socketID)
	s.mu.Unlock()
	if sshSess != nil {
		sshSess.close()
	}
}

func (ss *sshSession) isActive() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.active
}

func (ss *sshSession) close() {
	ss.mu.Lock()
	if !ss.active {
		ss.mu.Unlock()
		return
	}
	ss.active = false
	stdin := ss.stdin
	session := ss.session
	client := ss.client
	ss.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if session != nil {
		_ = session.Close()
	}
	if client != nil {
		_ = client.Close()
	}
}

func (s *sshServer) emitError(ctx context.Context, socketID, msg string) error {
	return s.emit(ctx, socketID, "ssh_error", msg)
}

func (s *sshServer) emit(ctx context.Context, socketID, event string, args ...any) error {
	s.mu.RLock()
	host := s.host
	s.mu.RUnlock()
	if host == nil {
		return nil
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return err
	}
	reply, err := host.Emit(ctx, &panel.EmitInstruction{
		Namespace: "/ssh",
		Target:    socketID,
		Event:     event,
		Payload:   payload,
	})
	if err != nil {
		return err
	}
	if reply.GetError() != "" {
		return errors.New(reply.GetError())
	}
	return nil
}

func (s *sshServer) log(ctx context.Context, level, msg string) {
	s.mu.RLock()
	host := s.host
	s.mu.RUnlock()
	if host == nil {
		return
	}
	_, _ = host.Log(ctx, &panel.LogRequest{Level: level, Message: msg})
}

func decodeFirstArg(payload []byte, out any) error {
	var args []json.RawMessage
	if err := json.Unmarshal(payload, &args); err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("missing payload")
	}
	return json.Unmarshal(args[0], out)
}

func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return os.ExpandEnv(path)
}
