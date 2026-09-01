import json
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"service": "inventory-api-python", "status": "ok"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print("inventory-api-python:", fmt % args)


if __name__ == "__main__":
    print("inventory-api-python escuchando en :8083")
    HTTPServer(("", 8083), Handler).serve_forever()
