package broker

import (
	"net/http"
	"testing"
)

func TestEndpointHTTPServerBoundsSlowClients(t *testing.T) {
	server := newEndpointHTTPServer(http.NotFoundHandler())
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout is unbounded")
	}
	if server.ReadTimeout <= 0 {
		t.Fatal("ReadTimeout is unbounded")
	}
	if server.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout is unbounded")
	}
	if server.MaxHeaderBytes <= 0 {
		t.Fatal("MaxHeaderBytes is unbounded")
	}
}
