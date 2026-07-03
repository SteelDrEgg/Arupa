//go:build wasip1

package main

import (
	"context"
	_ "embed"
	"net/http"

	panel "minimalpanel/pluginsdk/wasm/proto"
)

//go:embed pages/index.html
var indexHTML []byte

func main() {}

func init() {
	panel.RegisterPlugin(navigatorPlugin{})
}

type navigatorPlugin struct{}

func (navigatorPlugin) Register(_ context.Context, _ *panel.RegisterRequest) (*panel.RegisterReply, error) {
	return &panel.RegisterReply{
		Name:    "navigator",
		Version: "0.1.0",
		HttpRoutes: []*panel.HTTPRoute{
			{
				Method:    http.MethodGet,
				Pattern:   "/",
				Protected: true,
			},
		},
	}, nil
}

func (navigatorPlugin) HandleHTTP(_ context.Context, req *panel.HTTPRequest) (*panel.HTTPResponse, error) {
	if req.GetMethod() != http.MethodGet || req.GetPath() != "/" {
		return &panel.HTTPResponse{Status: http.StatusNotFound}, nil
	}
	return &panel.HTTPResponse{
		Status: http.StatusOK,
		Headers: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		Body: indexHTML,
	}, nil
}

func (navigatorPlugin) HandleSocketEvent(_ context.Context, _ *panel.SocketEvent) (*panel.SocketEventReply, error) {
	return &panel.SocketEventReply{}, nil
}

func (navigatorPlugin) HandlePluginMessage(_ context.Context, _ *panel.PluginMessage) (*panel.PluginMessageReply, error) {
	return &panel.PluginMessageReply{}, nil
}
