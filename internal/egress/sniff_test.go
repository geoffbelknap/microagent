package egress

import "testing"

func TestHTTPHostHeader(t *testing.T) {
	req := "GET /x HTTP/1.1\r\nHost: api.github.com:443\r\nAccept: */*\r\n\r\n"
	if got := httpHostHeader([]byte(req)); got != "api.github.com" {
		t.Fatalf("httpHostHeader = %q, want api.github.com", got)
	}
	if got := httpHostHeader([]byte("GET / HTTP/1.1\r\n\r\n")); got != "" {
		t.Fatalf("httpHostHeader = %q, want empty", got)
	}
}
