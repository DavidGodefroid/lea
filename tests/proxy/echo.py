# Fake OpenAI-compatible upstream for the key forwarder test: answers with what it received.
import http.server


class Echo(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        h = self.headers
        body = "".join(
            "%s=%s\n" % (k, v)
            for k, v in (
                ("path", self.path),
                ("authorization", "|".join(h.get_all("Authorization") or [])),
                ("x-api-key", h.get("X-Api-Key", "")),
                ("cookie", h.get("Cookie", "")),
                ("x-forwarded-for", h.get("X-Forwarded-For", "")),
                ("host", h.get("Host", "")),
            )
        ).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Set-Cookie", "session=upstream")
        self.end_headers()
        self.wfile.write(body)

    do_POST = do_GET

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("0.0.0.0", 8000), Echo).serve_forever()
