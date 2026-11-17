package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAlreadyCanceledLifetimeRejectsToolBeforeAPI(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	server := newServer(lifetime, mcpOptions{Version: "test"}, unitCaller(func(context.Context, string, any, any) error {
		called = true
		return nil
	}))
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "unit", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "mouse_scroll", Arguments: map[string]any{"amount": 1}})
	if err != nil || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "context canceled") || called {
		t.Fatalf("tool after lifetime cancellation result=%+v error=%v calledAPI=%v", result, err, called)
	}
}
