//go:build wasip1

package main

import (
	"context"
	"encoding/json"

	panel "minimalpanel/pluginsdk/wasm/proto"
)

func main() {}

func init() {
	panel.RegisterPlugin(webAssetsPlugin{})
}

type webAssetsPlugin struct{}

const webAssetsNamespace = "web-assets"

func (webAssetsPlugin) Register(ctx context.Context, _ *panel.RegisterRequest) (*panel.RegisterReply, error) {
	host := panel.NewHost()
	urls := map[string]string{
		"css_prefix":       "/assets/css/",
		"icon_prefix":      "/assets/icon/",
		"scheme_light_css": "/assets/css/scheme_light.css",
		"scheme_dark_css":  "/assets/css/scheme_dark.css",
	}
	urlsJSON, err := json.Marshal(urls)
	if err == nil {
		_, _ = host.KVSet(ctx, &panel.KVSetRequest{
			Namespace: webAssetsNamespace,
			Key:       "urls",
			Value:     urlsJSON,
		})
	}
	for k, v := range urls {
		_, _ = host.KVSet(ctx, &panel.KVSetRequest{
			Namespace: webAssetsNamespace,
			Key:       k,
			Value:     []byte(v),
		})
	}

	return &panel.RegisterReply{
		Name:    "web-assets",
		Version: "0.1.0",
		StaticMounts: []*panel.StaticMount{
			{
				Prefix:    "/assets/css/",
				Directory: "$PLUGIN_ROOT/assets/css",
			},
			{
				Prefix:    "/assets/icon/",
				Directory: "$PLUGIN_ROOT/assets/icon",
			},
		},
	}, nil
}

func (webAssetsPlugin) HandleHTTP(_ context.Context, _ *panel.HTTPRequest) (*panel.HTTPResponse, error) {
	return &panel.HTTPResponse{Status: 404}, nil
}

func (webAssetsPlugin) HandleSocketEvent(_ context.Context, _ *panel.SocketEvent) (*panel.SocketEventReply, error) {
	return &panel.SocketEventReply{}, nil
}
