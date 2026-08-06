#!/usr/bin/env python3
"""Hermetic OAuth2 token and protected-resource servers for the live E2E."""

import argparse
import json
import ssl
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def append_event(path, event):
    with open(path, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(event, sort_keys=True) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--bind", default="127.0.0.1")
    parser.add_argument("--token-port", type=int, required=True)
    parser.add_argument("--resource-port", type=int, required=True)
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--events", required=True)
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--client-secret", required=True)
    parser.add_argument("--token", required=True)
    args = parser.parse_args()

    class TokenHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", "0"))
            form = urllib.parse.parse_qs(self.rfile.read(length).decode("utf-8"))
            valid = (
                self.path == "/token"
                and form.get("grant_type") == ["client_credentials"]
                and form.get("client_id") == [args.client_id]
                and form.get("client_secret") == [args.client_secret]
                and form.get("scope") == ["read write"]
            )
            append_event(args.events, {
                "event": "token",
                "valid": valid,
                "grant_type": form.get("grant_type", [""])[0],
                "client_id": form.get("client_id", [""])[0],
                "scope": form.get("scope", [""])[0],
            })
            if not valid:
                self.send_response(400)
                self.end_headers()
                return
            body = json.dumps({
                "access_token": args.token,
                "token_type": "Bearer",
                "expires_in": 300,
            }).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format, *_args):
            return

    class ResourceHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            authorization = self.headers.get("Authorization", "")
            valid = authorization == "Bearer " + args.token
            append_event(args.events, {
                "event": "resource",
                "valid": valid,
                "placeholder_received": authorization == "Bearer guest-placeholder",
            })
            body = ("oauth2-live-ok\n" if valid else "unauthorized\n").encode("utf-8")
            self.send_response(200 if valid else 401)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, _format, *_args):
            return

    token_server = ThreadingHTTPServer((args.bind, args.token_port), TokenHandler)
    resource_server = ThreadingHTTPServer((args.bind, args.resource_port), ResourceHandler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(args.cert, args.key)
    resource_server.socket = context.wrap_socket(resource_server.socket, server_side=True)

    threading.Thread(target=token_server.serve_forever, daemon=True).start()
    print("oauth2_e2e_ready", flush=True)
    resource_server.serve_forever()


if __name__ == "__main__":
    main()
