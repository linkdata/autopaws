//go:build windows && (amd64 || arm64)

package capture

import (
	"context"

	"github.com/linkdata/autopaws/directshow"
	"github.com/linkdata/autopaws/v4l2"
)

func openSource(ctx context.Context, path string, options v4l2.Options) (Source, error) {
	source, err := directshow.OpenVideo(ctx, path, options)
	if err != nil {
		return nil, err
	}
	return source, nil
}
