//go:build (linux || darwin || windows) && (amd64 || arm64)

package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/linkdata/autopaws/apiwire"
)

func (server *Server) getAdapterUSBMode(ctx context.Context, data json.RawMessage) (any, error) {
	if err := params(data, &struct{}{}); err != nil {
		return nil, err
	}
	if server.options.Adapter == nil {
		return nil, apiwire.ErrUnavailable
	}
	mode, err := server.options.Adapter.GetUSBMode(ctx)
	if err != nil {
		return nil, err
	}
	return mode, nil
}

func (server *Server) setAdapterUSBMode(ctx context.Context, data json.RawMessage) (any, error) {
	var request struct {
		RawMode *int `json:"raw_mode"`
	}
	if err := params(data, &request); err != nil {
		return nil, err
	}
	if request.RawMode == nil {
		return nil, required("raw_mode")
	}
	switch *request.RawMode {
	case 0, 2, 128, 130:
	default:
		return nil, fmt.Errorf("%w: raw_mode must be 0, 2, 128 or 130", apiwire.ErrInvalidParams)
	}
	if server.options.Adapter == nil {
		return nil, apiwire.ErrUnavailable
	}
	if err := server.mouse.InvalidateMapping(ctx); err != nil {
		return nil, err
	}
	mode, err := server.options.Adapter.SetUSBMode(ctx, byte(*request.RawMode))
	if err != nil {
		return nil, err
	}
	return mode, nil
}
