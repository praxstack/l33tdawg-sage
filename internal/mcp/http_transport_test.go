package mcp

// HTTP transport integration tests.
//
// These tests cover the SSE + Streamable-HTTP transports without spinning
// up the full SAGE node. They use the same Server struct the stdio path
// uses, so we exercise the shared dispatch fn end-to-end.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestTransport(t *testing.T) *HTTPTransport {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	srv := NewServer("http://localhost:9999", priv)
	return NewHTTPTransport(srv)
}

func TestStreamableHTTP_BasicCall(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/streamable", transport.HandleStreamable)
	server := httptest.NewServer(mux)
	defer server.Close()

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	resp, err := http.Post(server.URL+"/v1/mcp/streamable", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rpcResp jsonRPCResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpcResp))
	require.Nil(t, rpcResp.Error)
	require.NotNil(t, rpcResp.Result)

	result, ok := rpcResp.Result.(map[string]any)
	require.True(t, ok, "result is map")
	tools, ok := result["tools"].([]any)
	require.True(t, ok, "tools is array")
	assert.Greater(t, len(tools), 5, "expect at least 5 tools registered")
}

func TestStreamableHTTP_Notification(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/streamable", transport.HandleStreamable)
	server := httptest.NewServer(mux)
	defer server.Close()

	// notifications/initialized has no ID → no response → HTTP 202.
	body := bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resp, err := http.Post(server.URL+"/v1/mcp/streamable", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestStreamableHTTP_BadJSON(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/streamable", transport.HandleStreamable)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/mcp/streamable", "application/json", bytes.NewBufferString("not-json"))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestSSE_HandshakeAndCall simulates ChatGPT's flow: open a long-lived SSE
// stream, receive the endpoint event, POST a tools/list call to that
// endpoint, read the response back off the SSE stream.
func TestSSE_HandshakeAndCall(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/sse", transport.HandleSSE)
	mux.HandleFunc("/v1/mcp/messages", transport.HandleSSEMessages)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/mcp/sse", nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	// Read events line by line until we see the "endpoint" event.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	endpointURL := ""
	currentEvent := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if currentEvent == "endpoint" {
				endpointURL = strings.TrimPrefix(line, "data: ")
			}
		case line == "":
			// blank line → end of event
			if endpointURL != "" {
				goto haveEndpoint
			}
		}
	}
haveEndpoint:
	require.NotEmpty(t, endpointURL, "expected endpoint event")
	require.Contains(t, endpointURL, "/v1/mcp/messages?sessionId=")

	// POST a tools/list to the messages endpoint.
	postURL := server.URL + endpointURL
	postBody := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, postBody)
	require.NoError(t, err)
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	postResp.Body.Close()
	require.Equal(t, http.StatusAccepted, postResp.StatusCode)

	// Now read the SSE stream until we see the message event with our response.
	currentEvent = ""
	gotResponse := false
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for SSE response")
		default:
		}
		if !scanner.Scan() {
			t.Fatalf("scanner error: %v", scanner.Err())
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && currentEvent == "message":
			payload := strings.TrimPrefix(line, "data: ")
			var rpcResp jsonRPCResponse
			require.NoError(t, json.Unmarshal([]byte(payload), &rpcResp))
			require.Nil(t, rpcResp.Error)
			result, ok := rpcResp.Result.(map[string]any)
			require.True(t, ok)
			require.Contains(t, result, "tools")
			gotResponse = true
			break loop
		}
	}
	assert.True(t, gotResponse)
}

func TestSSE_MessagesRejectsBadSession(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/messages", transport.HandleSSEMessages)
	server := httptest.NewServer(mux)
	defer server.Close()

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, err := http.Post(server.URL+"/v1/mcp/messages?sessionId=does-not-exist", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestSSE_MessagesRejectsMissingSession(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp/messages", transport.HandleSSEMessages)
	server := httptest.NewServer(mux)
	defer server.Close()

	body := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, err := http.Post(server.URL+"/v1/mcp/messages", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCORS_Preflight(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	handler := transport.CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("/v1/mcp/sse", handler)

	server := httptest.NewServer(mux)
	defer server.Close()

	req, err := http.NewRequest(http.MethodOptions, server.URL+"/v1/mcp/sse", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "https://chat.openai.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "Authorization")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "https://chat.openai.com", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Headers"), "Authorization")
}

func TestCORS_HeadersOnNormalRequest(t *testing.T) {
	transport := newTestTransport(t)
	mux := http.NewServeMux()
	handler := transport.CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	mux.Handle("/v1/mcp/streamable", handler)

	server := httptest.NewServer(mux)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/mcp/streamable", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", "https://chatgpt.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "https://chatgpt.com", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "Origin", resp.Header.Get("Vary"))
}

func TestCORS_NoOriginWildcard(t *testing.T) {
	transport := newTestTransport(t)
	handler := transport.CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/mcp/streamable", nil)
	// no Origin header
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "*", rr.Header().Get("Access-Control-Allow-Origin"))
}

// TestDispatchJSONRPC_Shared confirms that the dispatch fn called by the
// HTTP path is the SAME entry point the stdio path uses (no duplicate
// routing). We compare structurally rather than byte-for-byte because
// tools/list iterates a map and ordering is non-deterministic.
func TestDispatchJSONRPC_Shared(t *testing.T) {
	srv, _ := testServer(t)
	req := &jsonRPCRequest{JSONRPC: "2.0", ID: float64(7), Method: "initialize"}
	httpResp := srv.DispatchJSONRPC(context.Background(), req)
	stdioResp := srv.handleRequest(context.Background(), req)

	httpJSON, err := json.Marshal(httpResp)
	require.NoError(t, err)
	stdioJSON, err := json.Marshal(stdioResp)
	require.NoError(t, err)
	// initialize is deterministic — same response shape every call.
	assert.JSONEq(t, string(stdioJSON), string(httpJSON))
}

// _ keeps io.ReadAll referenced if unused above (defensive).
var _ = io.ReadAll
