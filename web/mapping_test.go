//go:build (linux || darwin || windows) && (amd64 || arm64)

package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"reflect"
	"strconv"
	"testing"

	"github.com/coder/websocket"
	"github.com/linkdata/autopaws/api"
	"github.com/linkdata/autopaws/apiclient"
	"github.com/linkdata/autopaws/hid"
)

func TestWebAndAPIApplyOneSharedGeometryWithoutTargetMovement(t *testing.T) {
	fixture := newFixture(t, Options{FPS: 120})
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	t.Cleanup(func() { _ = conn.CloseNow(); receive(t, done) })
	receiveMapping(t, conn)
	fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
	receive(t, conn.output)
	layout, err := hid.LoadLayout("us104", "")
	if err != nil {
		t.Fatal(err)
	}
	apiServer := api.New(hid.NewKeyboard(fixture.controller, layout), fixture.mouse, api.Options{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- apiServer.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := receive(t, served); err != nil {
			t.Error(err)
		}
		apiServer.Wait()
	})
	client := apiclient.New(listener.Addr().String())
	sendMapping(t, conn, `{"type":"mapping_set","mapping":{"geometry":{"offset":{"x":-1920,"y":200},"width":1920,"height":1080,"span":{"x":-1920,"y":0,"width":3840,"height":1280}}}}`)
	var webState mappingConfig
	for !webState.Ready || webState.Geometry == nil {
		webState = receiveMapping(t, conn)
	}
	var apiState hid.MappingState
	if err := client.Call(ctx, "get_mouse_mapping", nil, &apiState); err != nil {
		t.Fatal(err)
	}
	if !apiState.Ready || webState.Revision != strconv.FormatUint(apiState.Revision, 10) ||
		!reflect.DeepEqual(webState.Geometry, apiState.Geometry) || !reflect.DeepEqual(webState.Transform, apiState.Transform) {
		t.Fatalf("API and web did not share the applied geometry: web=%+v api=%+v", webState, apiState)
	}
	geometry := *apiState.Geometry
	geometry.Offset.Y = 0
	if err := client.Call(ctx, "set_mouse_mapping", hid.MappingSet{Geometry: &geometry}, nil); err != nil {
		t.Fatal(err)
	}
	for webState.Geometry == nil || webState.Geometry.Offset.Y != 0 || !webState.Ready {
		webState = receiveMapping(t, conn)
	}
	if err := client.Call(ctx, "get_mouse_mapping", nil, &apiState); err != nil {
		t.Fatal(err)
	}
	if webState.Revision != strconv.FormatUint(apiState.Revision, 10) || !reflect.DeepEqual(webState.Transform, apiState.Transform) {
		t.Fatal("API geometry update did not reach the viewer")
	}
	if prepares, reports := fixture.transport.snapshot(); prepares != 0 || len(reports) != 0 {
		t.Fatalf("geometry configuration emitted target input: %x", reports)
	}
}

func receiveMapping(t *testing.T, conn *fakeSocket) mappingConfig {
	t.Helper()
	message := receive(t, conn.output)
	var config struct {
		Mapping mappingConfig `json:"mapping"`
	}
	if message.kind != websocket.MessageText || json.Unmarshal(message.data, &config) != nil || config.Mapping.Revision == "" {
		t.Fatalf("missing mapping configuration: %d %s", message.kind, message.data)
	}
	return config.Mapping
}

func sendMapping(t *testing.T, conn *fakeSocket, message string) {
	t.Helper()
	select {
	case conn.input <- socketMessage{websocket.MessageText, []byte(message)}:
	case <-conn.closed:
		t.Fatal("mapping status closed the input connection")
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}

func TestSharedMappingChangesPublishBeforeFramesAndStaleGoalsStayConnected(t *testing.T) {
	fixture := newFixture(t, Options{FPS: 120})
	conn := newFakeSocket()
	done := make(chan struct{})
	go func() { fixture.server.serveConnection(conn); close(done) }()
	t.Cleanup(func() { _ = conn.CloseNow(); receive(t, done) })
	initial := receiveMapping(t, conn)
	fixture.source.frames <- sourceFrame(sourceJPEG(t), 1)
	first := receive(t, conn.output)
	if first.kind != websocket.MessageBinary || binary.LittleEndian.Uint64(first.data[16:]) != fixture.mouse.Mapping().Capture.Sequence {
		t.Fatal("video and mapping did not share the capture delivery sequence")
	}
	if err := fixture.mouse.SetMapping(t.Context(), hid.MappingSet{Mode: "extended"}); err != nil {
		t.Fatal(err)
	}
	changed := receiveMapping(t, conn)
	for changed.Mode != "extended" || changed.Revision == initial.Revision {
		changed = receiveMapping(t, conn)
	}
	if changed.Ready || changed.Revision == initial.Revision || changed.Mode != "extended" || changed.AfterSequence != "1" {
		t.Fatalf("shared mapping change was not published: %+v", changed)
	}
	sendMapping(t, conn, `{"type":"mousedown","x":100,"y":100,"buttons":1,"revision":"`+initial.Revision+`","frame_sequence":"1","frame_generation":"1"}`)
	status := receiveMapping(t, conn)
	if status.Error == "" {
		t.Fatal("stale positioned input did not return mapping status")
	}
	if _, frames := fixture.transport.snapshot(); len(frames) != 0 {
		t.Fatal("stale input touched hardware")
	}
	sendMapping(t, conn, `{"type":"mapping_reset"}`)
	reset := receiveMapping(t, conn)
	if reset.Ready || reset.Mode != "extended" || reset.Geometry != nil {
		t.Fatalf("mapping reset did not clear extended geometry: %+v", reset)
	}
	sendMapping(t, conn, `{"type":"mapping_set","mapping":{"mode":"legacy"}}`)
	for reset.Mode != "legacy" || !reset.Ready {
		reset = receiveMapping(t, conn)
	}
	fixture.source.frames <- sourceFrame(sourceJPEG(t), 2)
	if frame := receive(t, conn.output); frame.kind != websocket.MessageBinary {
		t.Fatal("video did not resume after mapping reset")
	}
}
