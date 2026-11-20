//go:build (linux || darwin) && (amd64 || arm64)

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"

	"github.com/linkdata/autopaws/capture"
)

func TestAudioCloseLoggingDistinguishesReportedCaptureFailure(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		name := "close-failure"
		if terminal {
			name = "reported-capture-failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var output bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
				source := newAudioTestSource(1)
				closeFailure := errors.New("PCM close fault")
				source.closeErr = closeFailure
				if terminal {
					source.closeErr = errors.Join(syscall.ENODEV, errors.New("PCM DROP fault"), closeFailure)
				}
				video, err := capture.New(capture.Options{Device: "/dev/fake-video", Width: 128, Height: 96})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				manager := newAudioManager(ctx, video, *sourceAudioOptions(source), logger)
				t.Cleanup(func() {
					cancel()
					receive(t, manager.done)
					if err := video.Close(); err != nil {
						t.Error(err)
					}
				})
				manager.subscribe()
				view := waitAudioView(t, manager, true)
				if terminal {
					manager.fail(view.run, syscall.ENODEV)
				} else {
					manager.unsubscribe()
				}
				receive(t, view.run.done)
				decoder := json.NewDecoder(&output)
				warnings, stopped := 0, 0
				for {
					var record struct {
						Level, Msg, Error string
					}
					if err := decoder.Decode(&record); err != nil {
						if !errors.Is(err, io.EOF) {
							t.Fatal(err)
						}
						break
					}
					if record.Level == "WARN" {
						warnings++
						want := "Web audio close failed"
						if terminal {
							want = "Web audio capture ended"
						}
						if record.Msg != want {
							t.Fatalf("misleading warning: %+v", record)
						}
					}
					if record.Msg == "Web audio source stopped" {
						stopped++
						if !terminal || record.Level != "DEBUG" || !strings.Contains(record.Error, syscall.ENODEV.Error()) ||
							!strings.Contains(record.Error, "PCM DROP fault") || !strings.Contains(record.Error, closeFailure.Error()) {
							t.Fatalf("joined shutdown details lost: %+v", record)
						}
					}
				}
				if warnings != 1 || (stopped == 1) != terminal {
					t.Fatalf("warnings=%d debug stopped=%d", warnings, stopped)
				}
			})
		})
	}
}
