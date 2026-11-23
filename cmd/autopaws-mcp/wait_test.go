package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func waitFrameReply(output any, frame sdkCapture) error {
	return unitAssign(output, map[string]any{
		"jpeg_b64": frame.jpeg, "width": frame.width, "height": frame.height,
		"target_width": frame.targetWidth, "target_height": frame.targetHigh,
	})
}

func waitCheckedImage(t *testing.T, request *http.Request) []byte {
	t.Helper()
	var body struct {
		Messages []struct {
			Content []struct {
				ImageURL map[string]string `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		t.Errorf("invalid vision request: %+v, %v", body, err)
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body.Messages[0].Content[1].ImageURL["url"], "data:image/jpeg;base64,"))
	if err != nil {
		t.Error(err)
	}
	return data
}

func waitReadReport(t *testing.T, result *mcp.CallToolResult) (observationResult, []byte) {
	t.Helper()
	var report observationResult
	if err := json.Unmarshal([]byte(sdkText(result)), &report); err != nil {
		t.Fatalf("invalid observation JSON: %v", err)
	}
	if result.StructuredContent == nil {
		t.Fatal("observation omitted structured content")
	}
	var image []byte
	for _, content := range result.Content {
		if part, ok := content.(*mcp.ImageContent); ok {
			if image != nil || part.MIMEType != "image/jpeg" {
				t.Fatal("observation must return at most one JPEG")
			}
			image = part.Data
		}
	}
	return report, image
}

func waitAssertFrame(t *testing.T, report observationResult, returned []byte, frame sdkCapture) {
	t.Helper()
	digest := sha256.Sum256(frame.jpeg)
	if !bytes.Equal(returned, frame.jpeg) || report.Frame == nil || report.Frame.ID == "" || report.Frame.SHA256 != hex.EncodeToString(digest[:]) || report.Frame.ImageWidth != frame.width || report.Frame.ImageHeight != frame.height || report.Frame.AgeMS < 0 {
		t.Fatalf("checked frame metadata/image mismatch: %+v", report.Frame)
	}
}

func TestWaitValidationSendsNoInput(t *testing.T) {
	for _, configured := range []bool{false, true} {
		var calls atomic.Int32
		options := mcpOptions{}
		if configured {
			options.Vision = &visionClient{}
		}
		session := sdkConnect(t, newServer(t.Context(), options, unitCaller(func(context.Context, string, any, any) error {
			calls.Add(1)
			return nil
		})))
		for _, tool := range []struct {
			name string
			args map[string]any
		}{
			{"type_text", map[string]any{"text": "fixture"}},
			{"send_key", map[string]any{"key": "enter"}},
			{"send_key_sequence", map[string]any{"steps": []any{map[string]any{"key": "enter"}}}},
			{"mouse_move", map[string]any{"x": 1, "y": 0, "relative": true}},
			{"mouse_click", map[string]any{}},
			{"mouse_drag", map[string]any{"start_x": 0, "start_y": 0, "end_x": 1, "end_y": 1}},
			{"mouse_scroll", map[string]any{"amount": 1}},
		} {
			invalid := []map[string]any{
				{"wait_timeout_ms": 100},
				{"wait_for": " "},
				{"wait_for": strings.Repeat("x", 2049)},
				{"wait_for": "condition", "wait_timeout_ms": 0},
				{"wait_for": "condition", "wait_timeout_ms": 60001},
			}
			if !configured {
				invalid = append(invalid, map[string]any{"wait_for": "condition"})
			}
			for _, wait := range invalid {
				args := make(map[string]any)
				for key, value := range tool.args {
					args[key] = value
				}
				for key, value := range wait {
					args[key] = value
				}
				result := sdkTool(t, session, tool.name, args, true)
				if !strings.Contains(sdkText(result), "wait") {
					t.Fatalf("%s failed after wait validation: %s", tool.name, sdkText(result))
				}
			}
		}
		if got := calls.Load(); got != 0 {
			t.Fatalf("invalid wait configuration made %d daemon calls", got)
		}
	}
}

func TestWaitReturnsFirstPositiveImageAndCommitsItsCoordinates(t *testing.T) {
	for _, test := range []struct {
		name               string
		falseFirst, resize bool
	}{{name: "first true"}, {name: "false then true", falseFirst: true}, {name: "resized analysis original result", resize: true}} {
		t.Run(test.name, func(t *testing.T) {
			frames := []sdkCapture{{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}}
			if test.falseFirst {
				frames = append(frames, sdkCapture{jpeg: sdkJPEG(t, 7, 4), width: 7, height: 4, targetWidth: 21, targetHigh: 13})
			}
			if test.resize {
				frames[0] = sdkCapture{jpeg: sdkJPEG(t, 128, 72), width: 128, height: 72, targetWidth: 1920, targetHigh: 1080}
			}
			var checks atomic.Int32
			vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
				index := int(checks.Add(1)) - 1
				if index >= len(frames) {
					t.Error("unexpected extra predicate evaluation")
					http.Error(writer, "extra check", http.StatusBadRequest)
					return
				}
				checked := waitCheckedImage(t, request)
				if test.resize {
					config, err := jpeg.DecodeConfig(bytes.NewReader(checked))
					if err != nil || config.Width != 64 || config.Height != 36 {
						t.Errorf("model image = %+v, %v", config, err)
					}
				} else if !bytes.Equal(checked, frames[index].jpeg) {
					t.Error("model checked a different image")
				}
				if index == len(frames)-1 {
					writeVisionResult(t, writer, `{"satisfied":true}`)
				} else {
					writeVisionResult(t, writer, `{"satisfied":false}`)
				}
			})
			if test.resize {
				vision.maxWidth = 64
			}
			var captures, inputs atomic.Int32
			caller := unitCaller(func(_ context.Context, method string, input, output any) error {
				if method == "capture_frame" {
					index := int(captures.Add(1)) - 1
					if inputs.Load() == 0 || index >= len(frames) {
						t.Error("capture was not a fresh post-input candidate")
						return context.Canceled
					}
					return waitFrameReply(output, frames[index])
				}
				encoded, err := json.Marshal(input)
				if err != nil {
					return err
				}
				if bytes.Contains(encoded, []byte("wait_")) {
					t.Error("MCP wait options reached the daemon")
				}
				if method == "mouse_move" {
					var point struct{ X, Y int }
					if err := json.Unmarshal(encoded, &point); err != nil {
						return err
					}
					last := frames[len(frames)-1]
					if point.X != last.targetWidth-1 || point.Y != last.targetHigh-1 {
						t.Errorf("committed coordinate mapping = %+v", point)
					}
				}
				inputs.Add(1)
				return unitAssign(output, map[string]bool{"sent": true})
			})
			session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
			result := sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "fixture condition"}, false)
			report, image := waitReadReport(t, result)
			if !report.Input.Sent || string(report.Input.Result) != `{"sent":true}` || report.Observation.Status != "condition_met" || report.Predicate.Satisfied == nil || !*report.Predicate.Satisfied || report.Predicate.Evaluations != len(frames) || checks.Load() != int32(len(frames)) {
				t.Fatalf("positive observation = %+v", report)
			}
			last := frames[len(frames)-1]
			waitAssertFrame(t, report, image, last)
			sdkTool(t, session, "mouse_move", map[string]any{"x": last.width - 1, "y": last.height - 1}, false)
		})
	}
}

func TestWaitObservationFailuresPreserveSuccessfulInput(t *testing.T) {
	for _, test := range []struct {
		name, wantStatus string
		captureError     bool
		httpError        bool
	}{
		{name: "capture error", wantStatus: "capture_error", captureError: true},
		{name: "invalid model output", wantStatus: "invalid_output"},
		{name: "provider error", wantStatus: "analysis_error", httpError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.Copy(io.Discard, request.Body)
				if test.httpError {
					http.Error(writer, "provider failure", http.StatusServiceUnavailable)
					return
				}
				writeVisionResult(t, writer, `{"satisfied":"true"}`)
			})
			frame := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
			var inputs atomic.Int32
			caller := unitCaller(func(_ context.Context, method string, _, output any) error {
				if method == "capture_frame" {
					if test.captureError {
						return errors.New("capture unavailable")
					}
					return waitFrameReply(output, frame)
				}
				inputs.Add(1)
				return unitAssign(output, map[string]bool{"sent": true})
			})
			session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
			report, image := waitReadReport(t, sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "condition"}, false))
			if !report.Input.Sent || string(report.Input.Result) != `{"sent":true}` || report.Observation.Status != test.wantStatus || report.Frame != nil || image != nil || inputs.Load() != 1 {
				t.Fatalf("observation failure lost input outcome: %+v", report)
			}
		})
	}
}

func TestWaitInputFailureDoesNotObserve(t *testing.T) {
	var calls atomic.Int32
	caller := unitCaller(func(_ context.Context, method string, _, _ any) error {
		calls.Add(1)
		if method != "send_key" {
			t.Errorf("failed input reached %s", method)
		}
		return errors.New("fixture input failure")
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: &visionClient{}}, caller))
	result := sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "condition"}, true)
	if calls.Load() != 1 || !strings.Contains(sdkText(result), "fixture input failure") {
		t.Fatalf("input failure = %s, calls %d", sdkText(result), calls.Load())
	}
}

func TestWaitTimeoutPreservesInputAndLastCheckedFrame(t *testing.T) {
	first := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	newer := sdkCapture{jpeg: sdkJPEG(t, 7, 4), width: 7, height: 4, targetWidth: 21, targetHigh: 13}
	var checks atomic.Int32
	canceled := make(chan struct{})
	vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		if checks.Add(1) == 1 {
			writeVisionResult(t, writer, `{"satisfied":false}`)
			return
		}
		<-request.Context().Done()
		close(canceled)
	})
	var captures, inputs atomic.Int32
	caller := unitCaller(func(_ context.Context, method string, _, output any) error {
		if method == "capture_frame" {
			if captures.Add(1) == 1 {
				return waitFrameReply(output, first)
			}
			return waitFrameReply(output, newer)
		}
		inputs.Add(1)
		return unitAssign(output, map[string]bool{"sent": true})
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
	result := sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "condition", "wait_timeout_ms": 200}, false)
	report, image := waitReadReport(t, result)
	if !report.Input.Sent || report.Observation.Status != "timeout" || report.Predicate.Satisfied == nil || *report.Predicate.Satisfied || report.Predicate.Evaluations != 2 || inputs.Load() != 1 {
		t.Fatalf("timeout lost successful input or checked predicate: %+v", report)
	}
	waitAssertFrame(t, report, image, first)
	<-canceled
	// The canceled second check must not establish the newer frame's coordinates.
	sdkTool(t, session, "mouse_move", map[string]any{"x": 6, "y": 3}, true)
	if inputs.Load() != 1 {
		t.Fatal("timeout committed an unchecked newer image")
	}
}

func TestWaitPeriodicallyRechecksUnchangedFrames(t *testing.T) {
	var checks, captures atomic.Int32
	var firstCompleted time.Time
	var mu sync.Mutex
	vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		if checks.Add(1) == 1 {
			writeVisionResult(t, writer, `{"satisfied":false}`)
			mu.Lock()
			firstCompleted = time.Now()
			mu.Unlock()
			return
		}
		mu.Lock()
		elapsed := time.Since(firstCompleted)
		mu.Unlock()
		if elapsed < 500*time.Millisecond {
			t.Errorf("unchanged frame rechecked after %s, before periodic interval", elapsed)
		}
		writeVisionResult(t, writer, `{"satisfied":true}`)
	})
	frame := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	caller := unitCaller(func(_ context.Context, method string, _, output any) error {
		if method == "capture_frame" {
			captures.Add(1)
			return waitFrameReply(output, frame)
		}
		return unitAssign(output, map[string]bool{"sent": true})
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
	report, image := waitReadReport(t, sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "condition", "wait_timeout_ms": 3000}, false))
	if report.Observation.Status != "condition_met" || report.Predicate.Satisfied == nil || !*report.Predicate.Satisfied || report.Predicate.Evaluations != 2 || checks.Load() != 2 || captures.Load() < 3 {
		t.Fatalf("unchanged frames starved or bypassed the model gate: %+v, captures %d", report, captures.Load())
	}
	waitAssertFrame(t, report, image, frame)
}

func TestWaitCaptureChangeCannotCommitCheckedCoordinates(t *testing.T) {
	frame := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		close(entered)
		<-release
		writeVisionResult(t, writer, `{"satisfied":true}`)
	})
	t.Cleanup(unblock)
	var inputs atomic.Int32
	caller := unitCaller(func(_ context.Context, method string, _, output any) error {
		if method == "capture_frame" {
			return waitFrameReply(output, frame)
		}
		if method == "send_key" || method == "mouse_move" {
			inputs.Add(1)
		}
		return unitAssign(output, map[string]bool{"sent": true})
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
	type outcome struct {
		result *mcp.CallToolResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx := sdkContext(t)
	go func() {
		result, err := session.client.CallTool(ctx, &mcp.CallToolParams{Name: "send_key", Arguments: map[string]any{"key": "enter", "wait_for": "condition"}})
		done <- outcome{result, err}
	}()
	<-entered
	sdkTool(t, session, "set_capture_resolution", map[string]any{"width": 640, "height": 480}, false)
	unblock()
	finished := <-done
	if finished.err != nil || finished.result.IsError {
		t.Fatalf("observation call = %+v, %v", finished.result, finished.err)
	}
	report, image := waitReadReport(t, finished.result)
	if !report.Input.Sent || report.Observation.Status != "capture_changed" || report.Predicate.Satisfied != nil || report.Frame != nil || image != nil {
		t.Fatalf("configuration change restored old image: %+v", report)
	}
	sdkTool(t, session, "mouse_move", map[string]any{"x": 0, "y": 0}, true)
	if inputs.Load() != 1 {
		t.Fatal("invalidated observation restored absolute coordinates")
	}
}

func TestWaitSerializesSessionInputAndCancelsQueuedAction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	vision := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		close(entered)
		<-release
		writeVisionResult(t, writer, `{"satisfied":true}`)
	})
	t.Cleanup(unblock)
	frame := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	var mu sync.Mutex
	var keys []string
	caller := unitCaller(func(_ context.Context, method string, input, output any) error {
		if method == "capture_frame" {
			return waitFrameReply(output, frame)
		}
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		var key struct{ Key string }
		if err := json.Unmarshal(data, &key); err != nil {
			return err
		}
		mu.Lock()
		keys = append(keys, key.Key)
		mu.Unlock()
		return unitAssign(output, map[string]bool{"sent": true})
	})
	server := newServer(t.Context(), mcpOptions{Vision: vision}, caller)
	queued, queueFinished := make(chan struct{}), make(chan struct{})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if params, ok := request.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" && strings.Contains(string(params.Arguments), `"b"`) {
				close(queued)
				defer close(queueFinished)
			}
			return next(ctx, method, request)
		}
	})
	first, independent := sdkConnect(t, server), sdkConnect(t, server)
	initial := make(chan error, 1)
	ctx := sdkContext(t)
	go func() {
		result, err := first.client.CallTool(ctx, &mcp.CallToolParams{Name: "send_key", Arguments: map[string]any{"key": "a", "wait_for": "condition"}})
		if err == nil && result.IsError {
			err = context.Canceled
		}
		initial <- err
	}()
	<-entered
	queuedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	queuedDone := make(chan error, 1)
	go func() {
		_, err := first.client.CallTool(queuedCtx, &mcp.CallToolParams{Name: "send_key", Arguments: map[string]any{"key": "b"}})
		queuedDone <- err
	}()
	<-queued
	// Another session can still act while the first session waits on its model.
	sdkTool(t, independent, "send_key", map[string]any{"key": "c"}, false)
	cancel()
	if err := <-queuedDone; err == nil {
		t.Fatal("queued action ignored cancellation")
	}
	<-queueFinished
	unblock()
	if err := <-initial; err != nil {
		t.Fatalf("initial observation = %v", err)
	}
	sdkTool(t, first, "send_key", map[string]any{"key": "d"}, false)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(keys, "") != "acd" {
		t.Fatalf("session serialization/cancellation sent keys %v", keys)
	}
}

func TestWaitActiveCancellationPreservesScreenshotState(t *testing.T) {
	for _, mode := range []string{"request", "lifetime"} {
		for _, priorImage := range []bool{false, true} {
			name := mode + "/no prior image"
			if priorImage {
				name = mode + "/prior image"
			}
			t.Run(name, func(t *testing.T) {
				entered, canceled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				vision := testVisionClient(t, func(_ http.ResponseWriter, request *http.Request) {
					_, _ = io.Copy(io.Discard, request.Body)
					close(entered)
					<-request.Context().Done()
					close(canceled)
				})
				prior := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
				candidate := sdkCapture{jpeg: sdkJPEG(t, 7, 4), width: 7, height: 4, targetWidth: 21, targetHigh: 13}
				var captures, inputs atomic.Int32
				lifetime, stop := context.WithCancel(t.Context())
				defer stop()
				owner := &toolServer{lifetime: lifetime, options: mcpOptions{Vision: vision}, sessions: make(map[*mcp.ServerSession]*screenState), instance: "fixture"}
				owner.caller = unitCaller(func(_ context.Context, method string, _, output any) error {
					if method == "capture_frame" {
						if captures.Add(1) == 1 && priorImage {
							return waitFrameReply(output, prior)
						}
						return waitFrameReply(output, candidate)
					}
					inputs.Add(1)
					return unitAssign(output, map[string]bool{"sent": true})
				})
				server := mcp.NewServer(&mcp.Implementation{Name: "wait-cancellation-test", Version: "test"}, nil)
				addTool(server, owner, "send_key", "Fixture input", false, owner.sendKey)
				addTool(server, owner, "capture_screen", "Fixture capture", true, owner.captureScreen)
				server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
					return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
						if params, ok := request.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" && params.Name == "send_key" {
							defer close(finished)
						}
						return next(ctx, method, request)
					}
				})
				session := sdkConnect(t, server)
				if priorImage {
					sdkTool(t, session, "capture_screen", map[string]any{}, false)
				}
				ctx, cancel := context.WithCancel(sdkContext(t))
				defer cancel()
				type outcome struct {
					result *mcp.CallToolResult
					err    error
				}
				done := make(chan outcome, 1)
				go func() {
					result, err := session.client.CallTool(ctx, &mcp.CallToolParams{Name: "send_key", Arguments: map[string]any{"key": "enter", "wait_for": "condition"}})
					done <- outcome{result, err}
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("wait did not start provider request")
				}
				if mode == "request" {
					cancel()
				} else {
					stop()
				}
				for _, signal := range []<-chan struct{}{canceled, finished} {
					select {
					case <-signal:
					case <-t.Context().Done():
						t.Fatal("active wait cancellation did not finish")
					}
				}
				completed := <-done
				if mode == "request" && !errors.Is(completed.err, context.Canceled) {
					t.Fatalf("request cancellation = %+v, %v", completed.result, completed.err)
				}
				if mode == "lifetime" && (completed.err != nil || completed.result == nil || !completed.result.IsError) {
					t.Fatalf("lifetime cancellation = %+v, %v", completed.result, completed.err)
				}
				owner.mu.Lock()
				defer owner.mu.Unlock()
				for _, state := range owner.sessions {
					dimensions, err := state.snapshot()
					if priorImage && (err != nil || dimensions != (screenDimensions{5, 3, 9, 7})) {
						t.Fatalf("cancellation changed prior coordinates: %+v, %v", dimensions, err)
					}
					if !priorImage && err == nil {
						t.Fatal("cancellation installed unchecked screenshot coordinates")
					}
				}
				if len(owner.sessions) != 1 || inputs.Load() != 1 {
					t.Fatalf("cancellation lost session or replayed input: %d sessions, %d inputs", len(owner.sessions), inputs.Load())
				}
			})
		}
	}
}

type waitRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip waitRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type waitDeadlineReader struct {
	ctx context.Context
	io.Reader
}

func (reader waitDeadlineReader) Read(data []byte) (int, error) {
	<-reader.ctx.Done()
	return reader.Reader.Read(data)
}

func TestWaitRejectsPositiveResponseAfterObservationDeadline(t *testing.T) {
	vision := &visionClient{model: "fixture-model", endpoint: "http://fixture.invalid/chat/completions", maxWidth: 960}
	vision.http = &http.Client{Transport: waitRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"choices":[{"finish_reason":"stop","message":{"content":"{\"satisfied\":true}"}}]}`
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
			Body: io.NopCloser(waitDeadlineReader{ctx: request.Context(), Reader: strings.NewReader(body)}),
		}, nil
	})}
	frame := sdkCapture{jpeg: unitJPEG(t), width: 5, height: 3, targetWidth: 9, targetHigh: 7}
	caller := unitCaller(func(_ context.Context, method string, _, output any) error {
		if method == "capture_frame" {
			return waitFrameReply(output, frame)
		}
		return unitAssign(output, map[string]bool{"sent": true})
	})
	session := sdkConnect(t, newServer(t.Context(), mcpOptions{Vision: vision}, caller))
	report, image := waitReadReport(t, sdkTool(t, session, "send_key", map[string]any{"key": "enter", "wait_for": "condition", "wait_timeout_ms": 50}, false))
	if !report.Input.Sent || report.Observation.Status != "timeout" || report.Predicate.Evaluations != 1 || report.Predicate.Satisfied != nil || report.Frame != nil || image != nil {
		t.Fatalf("late positive response was accepted: %+v", report)
	}
}

func TestVisualChangeThreshold(t *testing.T) {
	if visualChange([]byte{20, 20}, []byte{32, 8}) || !visualChange([]byte{20, 20}, []byte{33, 20}) || !visualChange([]byte{20}, []byte{20, 20}) {
		t.Fatal("visual gate did not honor absolute delta threshold and dimensions")
	}
}
