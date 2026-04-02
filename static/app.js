const treeEl = document.getElementById("tree");
const openFileEl = document.getElementById("openFile");
const statusEl = document.getElementById("status");
const saveBtn = document.getElementById("saveBtn");
const chatForm = document.getElementById("chatForm");
const chatInput = document.getElementById("chatInput");
const chatLog = document.getElementById("chatLog");
const editorHost = document.querySelector(".editor-wrap");

/** @type {import("codemirror").Editor | null} */
let cm = null;
let currentPath = "";

function setStatus(msg, err = false) {
  statusEl.textContent = msg;
  statusEl.style.color = err ? "#f48771" : "var(--muted)";
}

function modeForPath(p) {
  const ext = p.split(".").pop()?.toLowerCase() || "";
  const map = {
    js: "javascript",
    mjs: "javascript",
    ts: "javascript",
    py: "python",
    pyw: "python",
    pyi: "python",
    html: "htmlmixed",
    xml: "xml",
    css: "css",
    md: "markdown",
  };
  return map[ext] || null;
}

function ensureCM() {
  if (cm) return cm;
  const ta = document.createElement("textarea");
  editorHost.appendChild(ta);
  // @ts-ignore — global CodeMirror from CDN
  cm = CodeMirror.fromTextArea(ta, {
    theme: "dracula",
    lineNumbers: true,
    indentUnit: 4,
    tabSize: 4,
    lineWrapping: true,
  });
  const cmEl = editorHost.querySelector(".CodeMirror");
  if (cmEl) {
    cmEl.style.flex = "1";
    cmEl.style.minHeight = "0";
  }
  return cm;
}

async function loadTree(dir) {
  const q = new URLSearchParams();
  if (dir) q.set("path", dir);
  const res = await fetch("/api/tree?" + q.toString());
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

async function loadFile(path) {
  const q = new URLSearchParams({ path });
  const res = await fetch("/api/file?" + q.toString());
  if (!res.ok) throw new Error(await res.text());
  return res.text();
}

async function saveFile(path, content) {
  const res = await fetch("/api/file", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path, content }),
  });
  if (!res.ok) throw new Error(await res.text());
}

function renderTree(entries, basePath, depth) {
  const frag = document.createDocumentFragment();
  entries.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  for (const e of entries) {
    const row = document.createElement("div");
    row.className = "tree-item " + (e.isDir ? "dir" : "file");
    row.style.setProperty("--depth", String(depth));
    row.textContent = e.name;
    row.title = e.path;
    if (e.isDir) {
      row.addEventListener("click", async () => {
        try {
          const children = await loadTree(e.path);
          const block = document.createElement("div");
          block.appendChild(renderTree(children, e.path, depth + 1));
          row.insertAdjacentElement("afterend", block);
          row.replaceWith(block);
          block.prepend(row);
          row.style.cursor = "default";
        } catch (err) {
          setStatus(String(err), true);
        }
      });
    } else {
      row.addEventListener("click", async () => {
        try {
          setStatus("Loading…");
          const text = await loadFile(e.path);
          currentPath = e.path;
          openFileEl.textContent = e.path;
          const editor = ensureCM();
          editor.setValue(text);
          const mode = modeForPath(e.path);
          if (mode) editor.setOption("mode", mode);
          setStatus("Ready");
        } catch (err) {
          setStatus(String(err), true);
        }
      });
    }
    frag.appendChild(row);
  }
  return frag;
}

async function refreshRoot() {
  treeEl.innerHTML = "";
  try {
    const entries = await loadTree("");
    treeEl.appendChild(renderTree(entries, "", 0));
    setStatus("Ready");
  } catch (err) {
    setStatus(String(err), true);
  }
}

saveBtn.addEventListener("click", async () => {
  if (!cm || !currentPath) {
    setStatus("Open a file first", true);
    return;
  }
  try {
    setStatus("Saving…");
    await saveFile(currentPath, cm.getValue());
    setStatus("Saved");
  } catch (err) {
    setStatus(String(err), true);
  }
});

chatForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const msg = chatInput.value.trim();
  if (!msg) return;
  chatInput.value = "";
  const userDiv = document.createElement("div");
  userDiv.className = "msg";
  userDiv.textContent = "You: " + msg;
  chatLog.appendChild(userDiv);
  try {
    const payload = { message: msg };
    if (currentPath && cm) {
      payload.filePath = currentPath;
      payload.fileContent = cm.getValue();
    }
    const res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    const data = await res.json();
    const ans = document.createElement("div");
    ans.className = "msg" + (data.error ? " error" : "");
    ans.textContent = data.error ? data.error : "Assistant: " + (data.reply ?? "");
    chatLog.appendChild(ans);
    if (!data.error && Array.isArray(data.edits) && data.edits.length) {
      for (const ed of data.edits) {
        if (!ed.path) continue;
        await saveFile(ed.path, ed.content ?? "");
        if (cm && ed.path === currentPath) {
          cm.setValue(ed.content ?? "");
          const mode = modeForPath(ed.path);
          if (mode) cm.setOption("mode", mode);
        }
      }
      setStatus("Workspace updated from chat");
      await refreshRoot();
    }
  } catch (e) {
    const err = document.createElement("div");
    err.className = "msg error";
    err.textContent = String(e);
    chatLog.appendChild(err);
  }
  chatLog.scrollTop = chatLog.scrollHeight;
});

refreshRoot();
