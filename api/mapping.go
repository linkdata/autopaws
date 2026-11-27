//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"

	"github.com/linkdata/autopaws/hid"
)

func (server *Server) getMouseMapping(_ context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	return server.mouse.Mapping(), nil
}

func (server *Server) setMouseMapping(ctx context.Context, data json.RawMessage) (any, error) {
	var request hid.MappingSet
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if err := server.mouse.SetMapping(ctx, request); err != nil {
		return nil, err
	}
	return server.mouse.Mapping(), nil
}

func (server *Server) resetMouseMapping(ctx context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	if err := server.mouse.ResetMapping(ctx); err != nil {
		return nil, err
	}
	return server.mouse.Mapping(), nil
}
