// broker-harness runs a mock upstream + the egress broker for a real-microVM
// integration test. The upstream never echoes the secret to the caller (it
// reports MATCH/NOMATCH), so a guest can prove it reached the upstream with the
// live credential without ever holding it. Dev/integration tool.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/broker"
)

func main() {
	upAddr := flag.String("up", "127.0.0.1:18080", "mock upstream listen (host-local)")
	brAddr := flag.String("broker", "0.0.0.0:18081", "broker TCP listen (empty to disable)")
	brUnix := flag.String("broker-unix", "", "broker unix-socket listen (Firecracker host-side vsock endpoint <uds>_<port>)")
	flag.Parse()

	const liveSecret = "LIVE-SECRET-broker-demo-9f83a1c7"
	expected := "Bearer " + liveSecret

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("Authorization")
			match := got == expected
			log.Printf("[upstream] %s %s  authLen=%d  match=%v", r.Method, r.URL.Path, len(got), match)
			if match {
				_, _ = io.WriteString(w, "UPSTREAM-SAW-LIVE-SECRET\n")
			} else {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, "NOMATCH\n")
			}
		})
		log.Printf("[upstream] listening %s", *upAddr)
		log.Fatal(http.ListenAndServe(*upAddr, mux))
	}()

	resolve := func(name string) (string, bool) {
		if name == "demo" {
			return liveSecret, true
		}
		return "", false
	}
	tap := func(rec broker.TapRecord) {
		blob, _ := json.Marshal(rec)
		if strings.Contains(string(blob), liveSecret) {
			log.Printf("[broker][INVARIANT VIOLATION] live secret present in tap record: %s", blob)
			return
		}
		log.Printf("[broker][tap pre-swap] %s %s auth=%q  (no live secret in tap: OK)", rec.Method, rec.Path, rec.Headers.Get("Authorization"))
	}
	term, err := broker.NewTerminate("http://"+*upAddr, resolve, tap)
	if err != nil {
		log.Fatal(err)
	}
	conn := &broker.Connect{OnTap: func(rec broker.TapRecord) { log.Printf("[broker][tap] CONNECT %s", rec.Host) }}
	handler := broker.Handler(term, conn)

	if *brUnix != "" {
		_ = os.Remove(*brUnix)
		ln, err := net.Listen("unix", *brUnix)
		if err != nil {
			log.Fatalf("broker unix listen %s: %v", *brUnix, err)
		}
		log.Printf("[broker] listening unix %s (vsock host-side) -> upstream http://%s", *brUnix, *upAddr)
		go func() { log.Fatal(http.Serve(ln, handler)) }()
	}
	if *brAddr != "" {
		log.Printf("[broker] listening tcp %s -> upstream http://%s", *brAddr, *upAddr)
		log.Fatal(http.ListenAndServe(*brAddr, handler))
	}
	select {}
}
