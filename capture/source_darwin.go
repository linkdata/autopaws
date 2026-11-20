//go:build darwin && (amd64 || arm64)

package capture

import (
	"context"

	"github.com/linkdata/autopaws/avfoundation"
	"github.com/linkdata/autopaws/v4l2"
)

const recoverDefaultSource = true

func openSource(ctx context.Context, path string, options v4l2.Options) (Source, error) {
	source, err := avfoundation.OpenVideo(ctx, path, options)
	if err != nil {
		return nil, err
	}
	return source, nil
}
