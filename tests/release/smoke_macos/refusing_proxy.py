#!/usr/bin/env python3
"""A proxy that refuses everything and logs it (tests/release/smoke_macos.sh).

Anything that honours HTTP_PROXY / HTTPS_PROXY and tries to reach the network shows up as one line in the
log (CONNECT host:port, or the method and host); nothing is forwarded, so the smoke leaves the machine
with nothing. Usage: refusing_proxy.py PORT LOGFILE. LOGFILE.up is written once it listens.
"""
import http.server
import sys


class Refuse(http.server.BaseHTTPRequestHandler):
    def _log(self, line):
        with open(sys.argv[2], "a") as f:
            f.write(line + "\n")

    def _refuse(self):
        self._log("HTTP %s %s" % (self.command, self.headers.get("Host", "")))
        self.send_response(403)
        self.send_header("Content-Length", "0")
        self.end_headers()

    do_GET = do_POST = do_PUT = do_DELETE = do_HEAD = do_PATCH = do_OPTIONS = _refuse

    def do_CONNECT(self):
        self._log("CONNECT %s" % self.path)
        self.send_response(403)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Refuse)
    open(sys.argv[2] + ".up", "w").write("up")
    srv.serve_forever()
