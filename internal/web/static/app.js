const treeEl = document.getElementById("tree");
const openFileEl = document.getElementById("openFile");
const statusEl = document.getElementById("status");
const saveBtn = document.getElementById("saveBtn");
const acceptBtn = document.getElementById("acceptBtn");
const editorArea = document.getElementById("editorArea");
const runPythonBtn = document.getElementById("runPythonBtn");
const runConsoleOverlay = document.getElementById("runConsoleOverlay");
const runConsoleOut = document.getElementById("runConsoleOut");
const runConsoleTitle = document.getElementById("runConsoleTitle");
const runConsoleForm = document.getElementById("runConsoleForm");
const runConsoleLine = document.getElementById("runConsoleLine");
const runConsoleSend = document.getElementById("runConsoleSend");
const runConsoleClose = document.getElementById("runConsoleClose");
const agentForm = document.getElementById("agentForm");
const agentInput = document.getElementById("agentInput");
const agentChat = document.getElementById("agentChat");
const agentHistoryRefresh = document.getElementById("agentHistoryRefresh");
const agentUndoBtn = document.getElementById("agentUndoBtn");
const agentAttachments = document.getElementById("agentAttachments");
const agentPanel = document.querySelector(".agent-panel");

/** @type {HTMLButtonElement | null} */
const agentSubmitBtn = agentForm?.querySelector('button[type="submit"]') ?? null;

let agentRunBusy = false;
let agentUndoSnapshotAvailable = false;

function syncAgentToolbarButtons() {
  const busy = agentRunBusy;
  if (agentHistoryRefresh) agentHistoryRefresh.disabled = busy;
  if (agentUndoBtn) agentUndoBtn.disabled = busy || !agentUndoSnapshotAvailable;
}

async function refreshAgentUndoAvailability() {
  try {
    const res = await fetch("/api/agent-undo-available");
    if (!res.ok) {
      agentUndoSnapshotAvailable = false;
    } else {
      const d = await res.json();
      agentUndoSnapshotAvailable = !!d.available;
    }
  } catch {
    agentUndoSnapshotAvailable = false;
  }
  syncAgentToolbarButtons();
}

function setAgentPromptLocked(locked) {
  agentRunBusy = locked;
  if (agentInput) agentInput.disabled = locked;
  if (agentSubmitBtn) agentSubmitBtn.disabled = locked;
  syncAgentToolbarButtons();
}

/** @type {{ name: string, content: string }[]} */
let agentAttachedFiles = [];
const AGENT_ATTACH_MAX_FILES = 8;
const AGENT_ATTACH_MAX_BYTES = 400 * 1024;

/** @type {import("codemirror").Editor | null} */
let cm = null;
let currentPath = "";

/** Last accepted buffer for the open file (load, save, or Accept). Editor highlights differ from this until accepted. */
let acceptedBaselinePath = "";
let acceptedBaselineText = "";

/** Snapshot at agent run start for diffing disk updates into the editor. */
let preAgentEditorSnapshot = null;
let preAgentEditorPath = "";

const PENDING_LINE_ADD = "cm-pending-line-add";
const PENDING_LINE_CHG = "cm-pending-line-chg";
const MAX_DIFF_GRID = 2_800_000;

/** @type {WebSocket | null} */
let runWs = null;

/** @type {ReturnType<typeof setTimeout> | null} */
let pendingHighlightTimer = null;

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

/** @returns {string} workspace-relative directory for the open file, or "" for workspace root */
function cwdFromOpenFile() {
  if (!currentPath) return "";
  const i = currentPath.lastIndexOf("/");
  return i >= 0 ? currentPath.slice(0, i) : "";
}

const AGENT_SELECTION_MAX_CHARS = 32000;
const AGENT_LINE_AT_CURSOR_MAX_CHARS = 2000;

/**
 * Cursor / selection context for the agent (CodeMirror 5). Line numbers in the JSON are 1-based.
 * @param {import("codemirror").Editor} editor
 */
function getCodeMirrorAgentContext(editor) {
  if (!editor) return {};
  let from;
  let to;
  try {
    from = editor.getCursor("from");
    to = editor.getCursor("to");
  } catch {
    const c = editor.getCursor();
    from = c;
    to = c;
  }
  const startLine1 = from.line + 1;
  const endLine1 = to.line + 1;
  let selectionText = "";
  try {
    selectionText = editor.getSelection() || "";
  } catch {
    selectionText = "";
  }
  const hasSelection =
    selectionText.length > 0 && (from.line !== to.line || from.ch !== to.ch);
  if (hasSelection && selectionText.length > AGENT_SELECTION_MAX_CHARS) {
    selectionText =
      selectionText.slice(0, AGENT_SELECTION_MAX_CHARS) +
      "\n\n… (selection truncated for API size)";
  }
  const editorLine = from.line + 1;
  let lineAtCursor = "";
  if (!hasSelection && from.line >= 0) {
    try {
      lineAtCursor = editor.getLine(from.line) ?? "";
    } catch {
      lineAtCursor = "";
    }
    if (lineAtCursor.length > AGENT_LINE_AT_CURSOR_MAX_CHARS) {
      lineAtCursor =
        lineAtCursor.slice(0, AGENT_LINE_AT_CURSOR_MAX_CHARS) + "…";
    }
  }
  return {
    editorLine,
    selectionStartLine: startLine1,
    selectionEndLine: endLine1,
    hasSelection,
    selectionText: hasSelection ? selectionText : "",
    lineAtCursor: hasSelection ? "" : lineAtCursor,
  };
}

function splitDocLines(text) {
  if (text.length === 0) return [""];
  return text.split(/\r?\n/);
}

/**
 * Line-based LCS diff: returns Map of 0-based new line index -> PENDING_LINE_ADD | PENDING_LINE_CHG.
 * CHG is used for lines that replace deleted baseline lines; ADD for pure insertions.
 */
function pendingLineClassesByDiff(oldText, newText) {
  const a = splitDocLines(oldText);
  const b = splitDocLines(newText);
  const m = a.length;
  const n = b.length;
  const out = new Map();
  if (n === 0) return out;
  if (m * n > MAX_DIFF_GRID) {
    if (oldText === newText) return out;
    for (let j = 0; j < n; j++) out.set(j, PENDING_LINE_CHG);
    return out;
  }
  const dp = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      if (a[i - 1] === b[j - 1]) dp[i][j] = dp[i - 1][j - 1] + 1;
      else dp[i][j] = Math.max(dp[i - 1][j], dp[i][j - 1]);
    }
  }
  /** @type {("eq"|"ins"|"del")[]} */
  const ops = [];
  let i = m;
  let j = n;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && a[i - 1] === b[j - 1]) {
      ops.push("eq");
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      ops.push("ins");
      j--;
    } else {
      ops.push("del");
      i--;
    }
  }
  ops.reverse();
  let replacePending = false;
  let bj = 0;
  for (let k = 0; k < ops.length; k++) {
    const op = ops[k];
    if (op === "eq") {
      replacePending = false;
      bj++;
      continue;
    }
    if (op === "del") {
      replacePending = true;
      continue;
    }
    if (op === "ins") {
      const cls = replacePending ? PENDING_LINE_CHG : PENDING_LINE_ADD;
      out.set(bj, cls);
      bj++;
    }
  }
  return out;
}

function clearPendingLineHighlights(editor) {
  if (!editor) return;
  const lines = editor.lineCount();
  for (let ln = 0; ln < lines; ln++) {
    editor.removeLineClass(ln, "background", PENDING_LINE_ADD);
    editor.removeLineClass(ln, "background", PENDING_LINE_CHG);
  }
}

function editorHasPendingChanges() {
  return (
    !!cm &&
    !!currentPath &&
    currentPath === acceptedBaselinePath &&
    cm.getValue() !== acceptedBaselineText
  );
}

function updatePendingChrome() {
  const dirty = editorHasPendingChanges();
  if (saveBtn) saveBtn.classList.toggle("pending-save", dirty);
  if (acceptBtn) acceptBtn.disabled = !dirty;
}

function refreshPendingLineHighlights(editor) {
  if (!editor) return;
  clearPendingLineHighlights(editor);
  if (!currentPath || currentPath !== acceptedBaselinePath) {
    updatePendingChrome();
    return;
  }
  const cur = editor.getValue();
  if (cur === acceptedBaselineText) {
    updatePendingChrome();
    return;
  }
  const byLine = pendingLineClassesByDiff(acceptedBaselineText, cur);
  const n = editor.lineCount();
  for (const [lineIdx, cls] of byLine) {
    if (lineIdx >= 0 && lineIdx < n) editor.addLineClass(lineIdx, "background", cls);
  }
  updatePendingChrome();
}

function schedulePendingHighlightRefresh() {
  if (!cm) return;
  if (pendingHighlightTimer) clearTimeout(pendingHighlightTimer);
  pendingHighlightTimer = setTimeout(() => {
    pendingHighlightTimer = null;
    refreshPendingLineHighlights(cm);
  }, 90);
}

function setAcceptedBaseline(path, text) {
  acceptedBaselinePath = path;
  acceptedBaselineText = text;
  if (cm) refreshPendingLineHighlights(cm);
  else updatePendingChrome();
}

async function syncOpenFileAfterAgentRun() {
  const path = preAgentEditorPath;
  const snap = preAgentEditorSnapshot;
  preAgentEditorPath = "";
  preAgentEditorSnapshot = null;
  if (!path || snap === null || !cm || currentPath !== path) return;
  try {
    const disk = await loadFile(currentPath);
    if (disk !== snap) {
      cm.setValue(disk);
      acceptedBaselinePath = currentPath;
      acceptedBaselineText = snap;
      refreshPendingLineHighlights(cm);
    }
  } catch {
    /* keep editor as-is */
  }
}

function ensureCM() {
  if (cm) return cm;
  const ta = document.createElement("textarea");
  editorArea.appendChild(ta);
  // @ts-ignore — global CodeMirror from CDN
  cm = CodeMirror.fromTextArea(ta, {
    theme: "dracula",
    lineNumbers: true,
    indentUnit: 4,
    tabSize: 4,
    lineWrapping: true,
  });
  cm.on("change", () => schedulePendingHighlightRefresh());
  const cmEl = editorArea.querySelector(".CodeMirror");
  if (cmEl) {
    cmEl.style.flex = "1";
    cmEl.style.minHeight = "0";
  }
  return cm;
}

function openRunConsole() {
  runConsoleOverlay.hidden = false;
  runConsoleOut.replaceChildren();
  runConsoleLine.value = "";
  runConsoleLine.disabled = false;
  runConsoleSend.disabled = false;
  runConsoleTitle.textContent = "Running…";
  runConsoleLine.placeholder = "Type a line for input(), then Enter or Send";
  queueMicrotask(() => runConsoleLine.focus());
}

function setRunFinished(exitCode) {
  runConsoleTitle.textContent = `Finished (exit ${exitCode})`;
  runConsoleLine.disabled = true;
  runConsoleSend.disabled = true;
  runPythonBtn.disabled = false;
  runWs = null;
}

function closeRunConsole() {
  if (runWs && runWs.readyState === WebSocket.OPEN) {
    try {
      runWs.send(JSON.stringify({ type: "cancel" }));
    } catch (_) {
      /* ignore */
    }
    runWs.close();
  }
  runConsoleOverlay.hidden = true;
  runPythonBtn.disabled = false;
  runWs = null;
}

function runPythonInConsole() {
  const editor = ensureCM();
  const code = editor.getValue();
  if (!code.trim()) {
    setStatus("Editor is empty — add Python code to run", true);
    return;
  }
  if (runWs) {
    setStatus("A run is already in progress", true);
    return;
  }
  const cwd = cwdFromOpenFile();
  setStatus("Running Python…");
  openRunConsole();
  runPythonBtn.disabled = true;

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/ws/run-python`);
  runWs = ws;

  ws.onopen = () => {
    ws.send(JSON.stringify({ type: "start", code, cwd }));
  };

  ws.onmessage = (ev) => {
    let m;
    try {
      m = JSON.parse(ev.data);
    } catch {
      return;
    }
    if (m.type === "stdout" || m.type === "stderr") {
      const span = document.createElement("span");
      if (m.type === "stderr") span.className = "run-console-stderr";
      span.textContent = m.data;
      runConsoleOut.appendChild(span);
      runConsoleOut.scrollTop = runConsoleOut.scrollHeight;
      return;
    }
    if (m.type === "error") {
      const span = document.createElement("span");
      span.className = "run-console-stderr";
      span.textContent = `\n${m.message || "error"}\n`;
      runConsoleOut.appendChild(span);
      runConsoleOut.scrollTop = runConsoleOut.scrollHeight;
      setRunFinished(-1);
      setStatus(m.message || "Run failed", true);
      ws.close();
      return;
    }
    if (m.type === "exit") {
      const exitCode = typeof m.code === "number" ? m.code : -1;
      const span = document.createElement("span");
      span.className = "run-console-muted";
      span.textContent = `\n— Exit code: ${exitCode}\n`;
      runConsoleOut.appendChild(span);
      runConsoleOut.scrollTop = runConsoleOut.scrollHeight;
      setRunFinished(exitCode);
      setStatus("Run finished");
      ws.close();
    }
  };

  ws.onerror = () => {
    setStatus("WebSocket error", true);
  };

  ws.onclose = () => {
    runPythonBtn.disabled = false;
    if (runWs === ws) runWs = null;
    runConsoleLine.disabled = true;
    runConsoleSend.disabled = true;
    if (runConsoleTitle.textContent === "Running…") {
      runConsoleTitle.textContent = "Stopped";
      setStatus("Run stopped", true);
    }
  };
}

runConsoleForm.addEventListener("submit", (ev) => {
  ev.preventDefault();
  const line = runConsoleLine.value;
  if (!runWs || runWs.readyState !== WebSocket.OPEN) return;
  try {
    runWs.send(JSON.stringify({ type: "stdin", line }));
    runConsoleLine.value = "";
    runConsoleLine.focus();
  } catch (_) {
    /* ignore */
  }
});

runConsoleClose.addEventListener("click", () => closeRunConsole());

runConsoleOverlay.addEventListener("click", (ev) => {
  const t = ev.target;
  if (t && t.getAttribute && t.getAttribute("data-close-run-console") != null) {
    closeRunConsole();
  }
});

document.addEventListener("keydown", (ev) => {
  if (ev.key !== "Escape") return;
  if (!runConsoleOverlay || runConsoleOverlay.hidden) return;
  closeRunConsole();
});

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
          setAcceptedBaseline(e.path, text);
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
    setAcceptedBaseline(currentPath, cm.getValue());
    setStatus("Saved");
  } catch (err) {
    setStatus(String(err), true);
  }
});

if (acceptBtn) {
  acceptBtn.addEventListener("click", () => {
    if (!cm || !currentPath) {
      setStatus("Open a file first", true);
      return;
    }
    setAcceptedBaseline(currentPath, cm.getValue());
    setStatus("Changes accepted (not saved to disk unless you Save)");
  });
  acceptBtn.disabled = true;
}

runPythonBtn.addEventListener("click", () => {
  runPythonInConsole();
});

function renderAgentAttachments() {
  if (!agentAttachments) return;
  agentAttachments.replaceChildren();
  agentAttachedFiles.forEach((entry, i) => {
    const chip = document.createElement("span");
    chip.className = "attachment-chip";
    const nameEl = document.createElement("span");
    nameEl.className = "attachment-chip-name";
    nameEl.textContent = entry.name;
    nameEl.title = entry.name;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "attachment-chip-remove";
    btn.textContent = "×";
    btn.setAttribute("aria-label", `Remove ${entry.name}`);
    btn.addEventListener("click", () => {
      agentAttachedFiles.splice(i, 1);
      renderAgentAttachments();
    });
    chip.appendChild(nameEl);
    chip.appendChild(btn);
    agentAttachments.appendChild(chip);
  });
}

/**
 * @param {File} file
 * @returns {Promise<{ name: string, content: string } | null>}
 */
function readFileForAgent(file) {
  if (!file || !file.name) return Promise.resolve(null);
  if (file.size > AGENT_ATTACH_MAX_BYTES) {
    setStatus(
      `Skipped "${file.name}" (max ${Math.round(AGENT_ATTACH_MAX_BYTES / 1024)} KB per file for the agent)`,
      true
    );
    return Promise.resolve(null);
  }
  return new Promise((resolve) => {
    const r = new FileReader();
    r.onload = () => resolve({ name: file.name, content: String(r.result ?? "") });
    r.onerror = () => {
      setStatus(`Could not read ${file.name}`, true);
      resolve(null);
    };
    r.readAsText(file);
  });
}

/**
 * @param {{ summary?: string, error?: string, steps?: unknown[] }} data
 */
function formatAgentStepsText(data) {
  let out = "";
  if (data.error) {
    out += "Error: " + data.error + "\n\n";
  }
  out += "Summary: " + (data.summary ?? "") + "\n\n";
  const steps = Array.isArray(data.steps) ? data.steps : [];
  steps.forEach((step, i) => {
    out += "--- Step " + (i + 1) + " ---\n";
    if (step.rationale) out += "Rationale: " + step.rationale + "\n";
    out += "Python:\n" + (step.python ?? "") + "\n";
    const r = step.run || {};
    out += "exit_code: " + (typeof r.exitCode === "number" ? r.exitCode : "?") + "\n";
    if (r.stdout) out += "stdout:\n" + r.stdout + "\n";
    if (r.stderr) out += "stderr:\n" + r.stderr + "\n";
    if (r.error) out += "error: " + r.error + "\n";
    out += "\n";
  });
  if (!steps.length) out += "\n(No Python execution steps in this run.)\n";
  return out;
}

function formatPromptListTime(iso) {
  if (!iso || typeof iso !== "string") return "";
  const d = Date.parse(iso);
  if (Number.isNaN(d)) return iso;
  try {
    return new Date(d).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return iso;
  }
}

/**
 * @param {HTMLElement} assistantEl
 * @param {{ userMessage?: string, summary?: string, error?: string, stepCount?: number }} row
 */
function fillAssistantFromListRow(assistantEl, row) {
  assistantEl.classList.remove("chat-msg-pending", "chat-msg-error");
  const body = assistantEl.querySelector(".chat-msg-body");
  const hint = assistantEl.querySelector(".chat-steps-hint");
  if (!body) return;
  body.replaceChildren();
  if (row.error) {
    assistantEl.classList.add("chat-msg-error");
    const errLine = document.createElement("div");
    errLine.textContent = row.error;
    body.appendChild(errLine);
  } else {
    const sum = document.createElement("div");
    sum.textContent = row.summary?.trim() ? row.summary : "(no summary)";
    body.appendChild(sum);
  }
  if (hint) {
    const n = typeof row.stepCount === "number" ? row.stepCount : 0;
    hint.textContent = n ? `${n} code step(s)` : "";
  }
}

/** @param {{ id: number, createdAt: string, userMessage: string, summary?: string, error?: string, stepCount: number }} row */
function renderChatTurnFromListRow(row) {
  const turn = document.createElement("section");
  turn.className = "chat-turn";
  turn.dataset.promptId = String(row.id);

  const meta = document.createElement("div");
  meta.className = "chat-msg-meta";
  meta.textContent = formatPromptListTime(row.createdAt);

  const user = document.createElement("div");
  user.className = "chat-msg chat-msg-user";
  user.textContent = row.userMessage || "";

  const assistant = document.createElement("div");
  assistant.className = "chat-msg chat-msg-assistant";
  const body = document.createElement("div");
  body.className = "chat-msg-body";
  assistant.appendChild(body);
  const hint = document.createElement("div");
  hint.className = "chat-steps-hint";
  assistant.appendChild(hint);
  fillAssistantFromListRow(assistant, row);

  const details = document.createElement("details");
  details.className = "chat-transcript-details";
  const sum = document.createElement("summary");
  sum.textContent = "Full transcript";
  const pre = document.createElement("pre");
  pre.className = "chat-transcript-pre";
  pre.textContent = "Open to load…";
  details.appendChild(sum);
  details.appendChild(pre);

  details.addEventListener("toggle", async () => {
    if (!details.open || pre.dataset.loaded === "1") return;
    pre.textContent = "Loading…";
    try {
      const res = await fetch("/api/prompts?id=" + encodeURIComponent(String(row.id)));
      if (!res.ok) {
        pre.textContent = await res.text();
        return;
      }
      const detail = await res.json();
      pre.textContent = formatAgentStepsText(detail);
      pre.dataset.loaded = "1";
    } catch (e) {
      pre.textContent = String(e);
    }
  });

  assistant.appendChild(details);
  turn.appendChild(meta);
  turn.appendChild(user);
  turn.appendChild(assistant);
  return turn;
}

async function loadAgentChatHistory() {
  if (!agentChat) return;
  agentChat.replaceChildren();
  try {
    const res = await fetch("/api/prompts?limit=80");
    if (!res.ok) {
      const err = document.createElement("div");
      err.className = "agent-chat-empty";
      err.textContent = "Could not load history: " + (await res.text());
      agentChat.appendChild(err);
      return;
    }
    /** @type {Array<{ id: number, createdAt: string, userMessage: string, summary?: string, error?: string, stepCount: number }>} */
    const list = await res.json();
    if (!Array.isArray(list) || list.length === 0) {
      const empty = document.createElement("div");
      empty.className = "agent-chat-empty";
      empty.textContent =
        "No saved prompts yet. Each agent run is stored in the workspace database and will appear here.";
      agentChat.appendChild(empty);
      return;
    }
    const chronological = [...list].reverse();
    for (const row of chronological) {
      agentChat.appendChild(renderChatTurnFromListRow(row));
    }
  } catch (e) {
    const err = document.createElement("div");
    err.className = "agent-chat-empty";
    err.textContent = "Could not load history: " + String(e);
    agentChat.appendChild(err);
  }
  queueMicrotask(() => {
    agentChat.scrollTop = agentChat.scrollHeight;
  });
}

if (agentHistoryRefresh) {
  agentHistoryRefresh.addEventListener("click", () => {
    loadAgentChatHistory();
  });
}

if (agentUndoBtn) {
  agentUndoBtn.addEventListener("click", async () => {
    if (agentUndoBtn.disabled) return;
    try {
      setStatus("Restoring workspace…");
      const res = await fetch("/api/agent-undo", { method: "POST" });
      if (!res.ok) {
        setStatus((await res.text()).trim() || "Undo failed", true);
        return;
      }
      setStatus("Restored .py files to before the last agent run");
      await refreshRoot();
      if (currentPath && cm) {
        try {
          const t = await loadFile(currentPath);
          cm.setValue(t);
          setAcceptedBaseline(currentPath, t);
        } catch (err) {
          setStatus(String(err), true);
        }
      }
      await refreshAgentUndoAvailability();
    } catch (e) {
      setStatus(String(e), true);
    }
  });
}

if (agentPanel) {
  agentPanel.addEventListener("dragenter", (ev) => {
    ev.preventDefault();
    if (agentInput?.disabled) return;
    agentPanel.classList.add("agent-drop-hover");
  });
  agentPanel.addEventListener("dragleave", (ev) => {
    ev.preventDefault();
    const rt = ev.relatedTarget;
    if (rt && agentPanel.contains(rt)) return;
    agentPanel.classList.remove("agent-drop-hover");
  });
  agentPanel.addEventListener("dragover", (ev) => {
    ev.preventDefault();
    if (ev.dataTransfer) {
      ev.dataTransfer.dropEffect = agentInput?.disabled ? "none" : "copy";
    }
  });
  agentPanel.addEventListener("drop", async (ev) => {
    ev.preventDefault();
    agentPanel.classList.remove("agent-drop-hover");
    if (agentInput?.disabled) return;
    const files = ev.dataTransfer?.files;
    if (!files || files.length === 0) return;
    for (const file of Array.from(files)) {
      if (agentAttachedFiles.length >= AGENT_ATTACH_MAX_FILES) {
        setStatus(`At most ${AGENT_ATTACH_MAX_FILES} attached files`, true);
        break;
      }
      const entry = await readFileForAgent(file);
      if (entry) agentAttachedFiles.push(entry);
    }
    renderAgentAttachments();
    setStatus(
      agentAttachedFiles.length
        ? `${agentAttachedFiles.length} file(s) attached for the agent`
        : "Ready"
    );
  });
}

agentForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const msg = agentInput.value.trim();
  if (!msg || !agentChat) return;
  setStatus("Running CodeAct agent…");
  agentInput.value = "";

  if (cm && currentPath) {
    preAgentEditorSnapshot = cm.getValue();
    preAgentEditorPath = currentPath;
  } else {
    preAgentEditorSnapshot = null;
    preAgentEditorPath = "";
  }

  const turn = document.createElement("section");
  turn.className = "chat-turn";
  const meta = document.createElement("div");
  meta.className = "chat-msg-meta";
  meta.textContent = "Now";
  const user = document.createElement("div");
  user.className = "chat-msg chat-msg-user";
  user.textContent = msg;
  const assistant = document.createElement("div");
  assistant.className = "chat-msg chat-msg-assistant chat-msg-pending";
  assistant.textContent = "Running…";
  turn.appendChild(meta);
  turn.appendChild(user);
  turn.appendChild(assistant);

  const emptyHint = agentChat.querySelector(".agent-chat-empty");
  if (emptyHint) emptyHint.remove();
  agentChat.appendChild(turn);
  agentChat.scrollTop = agentChat.scrollHeight;

  const payload = { message: msg };
  const cwd = cwdFromOpenFile();
  if (cwd) payload.cwd = cwd;
  if (agentAttachedFiles.length) {
    payload.attachedFiles = agentAttachedFiles.map((a) => ({
      name: a.name,
      content: a.content,
    }));
  }
  if (currentPath && cm) {
    payload.filePath = currentPath;
    payload.fileContent = cm.getValue();
    Object.assign(payload, getCodeMirrorAgentContext(cm));
  }

  setAgentPromptLocked(true);
  try {
    const res = await fetch("/api/agent-code", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    const data = await res.json();
    assistant.classList.remove("chat-msg-pending");
    assistant.replaceChildren();
    if (data.error) {
      assistant.classList.add("chat-msg-error");
      const body = document.createElement("div");
      body.className = "chat-msg-body";
      body.textContent = data.error;
      assistant.appendChild(body);
      setStatus(data.error, true);
      await loadAgentChatHistory();
      return;
    }
    const body = document.createElement("div");
    body.className = "chat-msg-body";
    const sumEl = document.createElement("div");
    sumEl.textContent = data.summary?.trim() ? data.summary : "(no summary)";
    body.appendChild(sumEl);
    const hint = document.createElement("div");
    hint.className = "chat-steps-hint";
    const steps = Array.isArray(data.steps) ? data.steps : [];
    hint.textContent = steps.length ? `${steps.length} code step(s)` : "";
    assistant.appendChild(body);
    assistant.appendChild(hint);

    const details = document.createElement("details");
    details.className = "chat-transcript-details";
    const sum = document.createElement("summary");
    sum.textContent = "Full transcript";
    const pre = document.createElement("pre");
    pre.className = "chat-transcript-pre";
    pre.textContent = formatAgentStepsText(data);
    pre.dataset.loaded = "1";
    details.appendChild(sum);
    details.appendChild(pre);
    assistant.appendChild(details);

    setStatus("Agent finished");
    await refreshRoot();
    await syncOpenFileAfterAgentRun();
    await loadAgentChatHistory();
  } catch (e) {
    assistant.classList.remove("chat-msg-pending");
    assistant.classList.add("chat-msg-error");
    assistant.textContent = String(e);
    setStatus(String(e), true);
    await loadAgentChatHistory();
  } finally {
    setAgentPromptLocked(false);
    void refreshAgentUndoAvailability();
  }
});

refreshRoot();
void refreshAgentUndoAvailability();
loadAgentChatHistory();
