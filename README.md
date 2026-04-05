# Cursorlite — Python workspace

Small **Go** service that serves a web IDE over a workspace directory. The assistant is a single **CodeAct agent**: each turn the model returns **executable Python**; the server runs it under `WORKSPACE_ROOT`, sends back **stdout / stderr / exit code**, and repeats until the model sets `"done": true` or a step limit is hit. That is **code-as-action** with execution feedback.

You still edit files manually in the editor (**Save**, **Run**), or let the agent create or change them via Python (`pathlib`, `open()`, etc.).

**Design choices:** The agent loop runs **on the server** so one HTTP request carries the full transcript. **Python** is the action language—the same runtime as the **Run** button.

## Requirements

- **Go** 1.22+
- **Python** 3 on `PATH` (or set `PYTHON_BIN`)
- An **OpenAI-compatible** API key (`OPENAI_API_KEY`)

## Build and run (local)

From the repository root:

```bash
export WORKSPACE_ROOT=./workspace
export OPENAI_API_KEY=sk-...
go run .
```

Open [http://localhost:8080](http://localhost:8080).

| Variable | Meaning |
|----------|---------|
| `WORKSPACE_ROOT` | Directory for the file tree and Python cwd (default: `.`) |
| `LISTEN_ADDR` | HTTP listen address (default `:8080`) |
| `OPENAI_API_KEY` | API key |
| `OPENAI_BASE_URL` | Base URL (default `https://api.openai.com/v1`) |
| `OPENAI_MODEL` | Model name (default `gpt-4o-mini`) |
| `PYTHON_BIN` | Python executable if not `python3` / `python` on `PATH` |
| `PYTHON_RUN_TIMEOUT` | Per-run timeout, e.g. `60s` |
| `AGENT_MAX_STEPS` | Default max agent turns (capped at 15; request body can pass lower `maxSteps`) |

## Docker

```bash
cp .env.example .env
# Edit .env and set OPENAI_API_KEY
docker compose up --build
```

The host folder `./workspace` is mounted at `/workspace` in the container. The UI is on port **8080**.

## How “code as action” works here

The **CodeAct** panel sends a goal to `POST /api/agent-code`. The backend asks the model for a **single JSON object** per turn (`rationale`, `python`, `done`, `summary`). When `done` is false and `python` is non-empty, the server runs that code with `python -u -` inside `WORKSPACE_ROOT` (or a relative `cwd` from the request). The next message in the internal conversation is the **execution result** (exit code and streams). Manual **Run** uses the same execution helper (`POST /api/run-python`).

## Security and limits

Generated code is **not** a true sandbox: it runs as the server user with access to everything under `WORKSPACE_ROOT` (and whatever the process can reach). Mitigations in this POC: paths are constrained with `safeRel` / `underRoot`, Python output and run duration are capped, and agent turns are capped. **Do not** point `WORKSPACE_ROOT` at sensitive data for untrusted models.

## Demo script (interview)

1. Start the server with `WORKSPACE_ROOT=./workspace` and a valid `OPENAI_API_KEY`. Open the UI and confirm the tree shows `workspace/`.
2. In **CodeAct agent**, enter: *List the files in the workspace root and print their names.* Run the agent; confirm the transcript shows Python + stdout and a short summary.
3. Ask the agent to *create a small `demo_agent.py` that prints a message*; refresh the tree and open the file.

## Project layout

- `main.go` — HTTP API, tree/files, Python runner, LLM helpers, agent route registration
- `agent.go` — CodeAct loop and prompts
- `static/` — embedded UI

See [`.env.example`](.env.example) for environment variable names.
