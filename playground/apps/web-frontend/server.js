const http = require("http");
const fs = require("fs");
const path = require("path");

const server = http.createServer((req, res) => {
  const html = fs.readFileSync(path.join(__dirname, "index.html"));
  res.writeHead(200, { "Content-Type": "text/html" });
  res.end(html);
});

server.listen(5173, () => {
  console.log("web-frontend escuchando en :5173");
});
