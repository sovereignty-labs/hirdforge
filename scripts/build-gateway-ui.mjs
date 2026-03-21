import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const root = process.cwd();
const sourcePath = path.join(root, "cmd", "gateway", "index.html");
const buildDir = path.join(root, "cmd", "gateway", "build");
const buildPath = path.join(buildDir, "index.html");

const html = await readFile(sourcePath, "utf8");

const requiredSnippets = [
  "<div id=\"root\"></div>",
  "ReactDOM.createRoot(document.getElementById('root')).render(<App/>);",
  "function App(){",
  "function ChatPanel(",
  "function MissionControlView(",
  "/ws/events"
];

for (const snippet of requiredSnippets) {
  if (!html.includes(snippet)) {
    throw new Error(`gateway UI build validation failed: missing snippet ${snippet}`);
  }
}

await mkdir(buildDir, { recursive: true });
await writeFile(buildPath, html, "utf8");

console.log(`Built gateway UI to ${path.relative(root, buildPath)}`);
