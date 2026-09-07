#!/usr/bin/env python3
"""Probe: does Onshape attempt the 3Dconnexion nlproxy handshake on Linux?

Impersonates just enough of the NL-Proxy to see whether the browser knocks.
Logs every request, including the WebSocket upgrade that follows a successful
nlproxy query.
"""
import http.server, ssl, sys, datetime

HOST, PORT = "127.51.68.120", 8181
CERT, KEY = sys.argv[1], sys.argv[2]

def stamp():
    return datetime.datetime.now().strftime("%H:%M:%S")

class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _cors(self):
        origin = self.headers.get("Origin", "*")
        self.send_header("Access-Control-Allow-Origin", origin)
        self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "*")

    def log_message(self, *a):
        pass  # we do our own

    def _note(self, kind):
        origin = self.headers.get("Origin", "-")
        upgrade = self.headers.get("Upgrade", "")
        tag = "  << WEBSOCKET UPGRADE >>" if upgrade.lower() == "websocket" else ""
        print(f"[{stamp()}] {kind} {self.path}  origin={origin}{tag}", flush=True)

    def do_OPTIONS(self):
        self._note("OPTIONS")
        self.send_response(204); self._cors()
        self.send_header("Content-Length", "0"); self.end_headers()

    def do_GET(self):
        self._note("GET    ")
        if self.path.startswith("/3dconnexion/nlproxy"):
            body = b'{"port": 8181, "version": "1.4.8.21486"}'
            print(f"[{stamp()}] *** SNIFF IS GONE — Onshape queried nlproxy ***", flush=True)
            self.send_response(200); self._cors()
            self.send_header("Content-Type", "application/json; charset=utf-8")
        else:
            body = (b"<h1>SpaceMouse probe</h1><p>Certificate accepted. "
                    b"Now open an Onshape document and watch the terminal.</p>")
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers(); self.wfile.write(body)

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(CERT, KEY)
srv = http.server.ThreadingHTTPServer((HOST, PORT), Handler)
srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
print(f"listening on https://{HOST}:{PORT}  (ctrl-c to stop)", flush=True)
srv.serve_forever()
