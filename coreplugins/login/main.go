//go:build wasip1

package main

import (
	"context"

	panel "minimalpanel/pluginsdk/wasm/proto"
)

func main() {}

func init() {
	panel.RegisterPlugin(loginPlugin{})
}

type loginPlugin struct{}

func (loginPlugin) Register(_ context.Context, _ *panel.RegisterRequest) (*panel.RegisterReply, error) {
	return &panel.RegisterReply{
		Name:    "login",
		Version: "0.1.0",
		StaticMounts: []*panel.StaticMount{
			{
				Prefix:    "/pages/login.html",
				Directory: "$PLUGIN_ROOT/pages/login.html",
			},
			{
				Prefix:    "/pages/logout.html",
				Directory: "$PLUGIN_ROOT/pages/logout.html",
			},
		},
	}, nil
}

func (loginPlugin) HandleHTTP(_ context.Context, _ *panel.HTTPRequest) (*panel.HTTPResponse, error) {
	return &panel.HTTPResponse{Status: 404}, nil
}

func (loginPlugin) HandleSocketEvent(_ context.Context, _ *panel.SocketEvent) (*panel.SocketEventReply, error) {
	return &panel.SocketEventReply{}, nil
}
