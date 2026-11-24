package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testVisionClient(t *testing.T, handler http.HandlerFunc) *visionClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newVisionClient(visionConfig{API: server.URL + "/v1", Model: "fixture-model", MaxWidth: 960})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeVisionResult(t *testing.T, writer http.ResponseWriter, content string) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
		"finish_reason": "stop", "message": map[string]string{"content": content},
	}}}); err != nil {
		t.Error(err)
	}
}

func TestVisionConfiguration(t *testing.T) {
	if client, err := newVisionClient(visionConfig{}); err != nil || client != nil {
		t.Fatalf("disabled vision = %+v, %v", client, err)
	}
	valid := visionConfig{API: "https://example.test/v1/", Model: "fixture-model", MaxWidth: 960}
	for _, test := range []struct {
		name   string
		change func(*visionConfig)
	}{
		{"missing API", func(c *visionConfig) { c.API = "" }},
		{"missing model", func(c *visionConfig) { c.Model = " " }},
		{"invalid URL", func(c *visionConfig) { c.API = "://bad" }},
		{"unsupported scheme", func(c *visionConfig) { c.API = "file:///tmp/model" }},
		{"credentials", func(c *visionConfig) { c.API = "https://user:password@example.test/v1" }},
		{"fragment", func(c *visionConfig) { c.API += "#fragment" }},
		{"token newline", func(c *visionConfig) { c.Token = "fixture\r\ntoken" }},
		{"small width", func(c *visionConfig) { c.MaxWidth = 63 }},
		{"large width", func(c *visionConfig) { c.MaxWidth = 4097 }},
		{"invalid options", func(c *visionConfig) { c.RequestJSON = "{" }},
		{"null options", func(c *visionConfig) { c.RequestJSON = "null" }},
		{"array options", func(c *visionConfig) { c.RequestJSON = "[]" }},
		{"model override", func(c *visionConfig) { c.RequestJSON = `{"model":"other"}` }},
		{"messages override", func(c *visionConfig) { c.RequestJSON = `{"messages":[]}` }},
		{"stream override", func(c *visionConfig) { c.RequestJSON = `{"stream":true}` }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.change(&config)
			if _, err := newVisionClient(config); err == nil {
				t.Fatal("invalid vision configuration accepted")
			}
		})
	}
	for _, endpoint := range []string{"https://example.test/v1/", "https://example.test/v1/chat/completions"} {
		config := valid
		config.API = endpoint
		client, err := newVisionClient(config)
		if err != nil || client.endpoint != "https://example.test/v1/chat/completions" {
			t.Fatalf("endpoint = %+v, %v", client, err)
		}
	}
	config := valid
	config.API = "https://example.test/v1?api-version=fixture"
	client, err := newVisionClient(config)
	if err != nil || client.endpoint != "https://example.test/v1/chat/completions?api-version=fixture" {
		t.Fatalf("provider query = %+v, %v", client, err)
	}
}

func TestVisionRequestOptionsAndAuthentication(t *testing.T) {
	jpegData := unitJPEG(t)
	client := testVisionClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer fixture-token" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected vision request: %s %s headers %v", request.Method, request.URL.Path, request.Header)
		}
		var body struct {
			Model           string         `json:"model"`
			Stream          bool           `json:"stream"`
			MaxTokens       int            `json:"max_tokens"`
			ReasoningEffort string         `json:"reasoning_effort"`
			ResponseFormat  map[string]any `json:"response_format"`
			Messages        []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string            `json:"type"`
					Text     string            `json:"text"`
					ImageURL map[string]string `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "fixture-model" || body.Stream || body.MaxTokens != 64 || body.ReasoningEffort != "none" || body.ResponseFormat["type"] != "json_schema" || len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Errorf("unexpected provider options: %+v", body)
			return
		}
		content := body.Messages[0].Content
		if body.Messages[0].Role != "user" || content[0].Type != "text" || content[0].Text != visionInstructions+"fixture condition" || content[1].ImageURL["url"] != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(jpegData) {
			t.Error("vision request changed its condition or image")
		}
		writeVisionResult(t, writer, `{"satisfied":true}`)
	})
	client.token = "fixture-token"
	client.options = map[string]json.RawMessage{"max_tokens": json.RawMessage(`64`), "reasoning_effort": json.RawMessage(`"none"`)}
	if satisfied, err := client.check(t.Context(), jpegData, "fixture condition"); err != nil || !satisfied {
		t.Fatalf("vision check = %t, %v", satisfied, err)
	}
}

func TestVisionStrictOutput(t *testing.T) {
	for _, content := range []string{
		``, `null`, `true`, `[]`, `{}`, `{"satisfied":null}`, `{"satisfied":1}`,
		`{"satisfied":"true"}`, `{"other":true}`, `{"satisfied":true,"extra":1}`,
		`{"satisfied":true,"satisfied":false}`, `{"satisfied":true} {"satisfied":false}`,
		"```json\n{\"satisfied\":true}\n```",
	} {
		if _, err := parseVisionResult(content); !errors.Is(err, errInvalidVisionOutput) {
			t.Errorf("invalid output %q accepted: %v", content, err)
		}
	}
	for _, value := range []bool{false, true} {
		content, _ := json.Marshal(map[string]bool{"satisfied": value})
		if got, err := parseVisionResult(" \n" + string(content) + "\t"); err != nil || got != value {
			t.Fatalf("boolean %t = %t, %v", value, got, err)
		}
	}
	for _, response := range []string{
		`not JSON`, `{}`, `{"choices":[]}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"{\"satisfied\":true}"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"","reasoning_content":"{\"satisfied\":true}"}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"{\"satisfied\":true}"}},{"finish_reason":"stop","message":{"content":"{\"satisfied\":false}"}}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			client := testVisionClient(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(response)) })
			if _, err := client.check(t.Context(), unitJPEG(t), "condition"); !errors.Is(err, errInvalidVisionOutput) {
				t.Fatalf("malformed response accepted: %v", err)
			}
		})
	}
}

func TestVisionProviderFailureAndCancellation(t *testing.T) {
	t.Run("HTTP failure", func(t *testing.T) {
		client := testVisionClient(t, func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "provider response", http.StatusServiceUnavailable)
		})
		if _, err := client.check(t.Context(), unitJPEG(t), "condition"); err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "provider response") {
			t.Fatalf("provider failure = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		entered := make(chan struct{})
		canceled := make(chan struct{})
		client := testVisionClient(t, func(_ http.ResponseWriter, request *http.Request) {
			_, _ = io.Copy(io.Discard, request.Body)
			close(entered)
			<-request.Context().Done()
			close(canceled)
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		jpegData := unitJPEG(t)
		go func() { _, err := client.check(ctx, jpegData, "condition"); done <- err }()
		<-entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("HTTP cancellation = %v", err)
		}
		<-canceled
	})
}

func TestVisionPreparationPreservesReturnedImage(t *testing.T) {
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 200, 100)), nil); err != nil {
		t.Fatal(err)
	}
	client := &visionClient{maxWidth: 64}
	decoded, thumbnail, err := prepareVisionFrame(t.Context(), data.Bytes())
	if err != nil || len(thumbnail) != 160*90 {
		t.Fatalf("prepare = %+v, thumbnail %d, %v", decoded, len(thumbnail), err)
	}
	prepared, err := client.image(t.Context(), data.Bytes(), decoded)
	if err != nil {
		t.Fatal(err)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(prepared))
	if err != nil || config.Width != 64 || config.Height != 32 {
		t.Fatalf("resized image = %+v, %v", config, err)
	}
	client.maxWidth = 960
	prepared, err = client.image(t.Context(), data.Bytes(), decoded)
	if err != nil || !bytes.Equal(prepared, data.Bytes()) {
		t.Fatalf("unscaled JPEG changed: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := prepareVisionFrame(ctx, data.Bytes()); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare cancellation = %v", err)
	}
	client.maxWidth = 64
	if _, err := client.image(ctx, data.Bytes(), decoded); !errors.Is(err, context.Canceled) {
		t.Fatalf("resize cancellation = %v", err)
	}
	if _, _, err := prepareVisionFrame(t.Context(), []byte("not JPEG")); err == nil {
		t.Fatal("invalid JPEG accepted")
	}
}
