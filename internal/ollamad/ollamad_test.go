package ollamad

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelReadyRequiresEmbeddingProbe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dim       int
		wantReady bool
	}{
		{name: "tag only is not enough", dim: 0, wantReady: false},
		{name: "wrong dimension is refused", dim: 12, wantReady: false},
		{name: "expected embedding dimension passes", dim: ModelDimension, wantReady: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"models": []map[string]any{{"name": ModelName + ":latest"}},
					})
				case "/api/embed":
					if tc.dim == 0 {
						http.Error(w, "model not loadable", http.StatusInternalServerError)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"embeddings": [][]float32{make([]float32, tc.dim)},
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			m := New(t.TempDir())
			m.port = testServerPort(t, srv)
			assert.Equal(t, tc.wantReady, m.ModelReady(context.Background()))
		})
	}
}

func testServerPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	return n
}
