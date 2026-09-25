# DataTug Chat PoC

The [original Phase 1 request](chat-phase1-original-prompt.md) is preserved
verbatim for historical context.
The [Phase 3 request and its later continuation](chat-phase3-original-prompt.md)
are preserved for historical context.
The [original Phase 4 request](chat-phase4-original-prompt.md) is also preserved
verbatim; this link does not imply Phase 4 has been implemented.

Phase 2 added durable, session-scoped chat state and RecordSet snapshots.
See [the Phase 2 design](chat-phase2-design.md) for storage and lifecycle
details. Phase 3 adds session-scoped workspace state: project objects,
structured attachments, Views/Selections, and docks. The original AI → DTQL
→ secure execution → grid path below is unchanged.

This phase proves one vertical slice:

```text
user -> ai/agent.Loop -> run_dtql tool -> secureread.Executor -> DALgo backend
     -> secureread.Result -> tui/grid grid
```

The model never executes SQL and never renders rows. Its only data tool accepts
a DTQL YAML document. DataTug validates that document with `dtql.Deserialize`,
applies the normal access-policy path, executes it through the configured DALgo
adapter, and gives the chat UI a structured `secureread.Result`.

The terminal grid uses `github.com/strongo/aichat`'s `tui/grid` package (built
on Charm's `bubbletea`/`bubbles`) for table layout, pagination, and horizontal
overflow. DataTug keeps a small adapter (`gridState`, `pkg/chat/grid_state.go`)
around that component so chat/query domain types remain independent of the
grid library; DataTug continues to own the result-card title, footer,
scrollbar, focus navigation, and terminal styling.

## Run

From a DataTug project directory with a scanned schema and a local environment:

```sh
datatug chat --role admin
```

Or name the project explicitly:

```sh
datatug chat \
  --project /path/to/project \
  --env local \
  --role admin
```

The default model is `gpt-5.6-luna`, with low reasoning effort for this small
translation task. `OPENAI_API_KEY` must be present. Model selection stays
configurable, for example:

```sh
datatug chat --model ollama/qwen3:4b
```

### Shared AI API and interaction telemetry

To use the shared Sneat AI API instead of a personal model API key, sign in
with DataTug's own device flow and select the `cloud` model:

```sh
datatug auth login
datatug chat --model cloud
```

`cloud` sends the chat request to `https://api.sneat.cloud/v0/ai/chat` with
the DataTug-scoped Firebase session and `X-AI-Product: datatug`. A custom
`--base-url` must be an HTTPS `/v0/` API URL, or a loopback HTTP `/v0/` URL
for local development. A login made with `auth login --insecure-storage`
requires `chat --insecure-storage`. Direct BYOK models remain the default.

The cloud request carries a random installation UUID stored under the OS user
config directory at `datatug/installation_id`, CLI build version, OS,
architecture, and the existing durable chat ID as `conversationId`. The ID is
not based on hardware and resets when that configuration is removed. Each
turn also has a fresh interaction UUID, shared by its AI calls and a final
client observation. Command turns are observed as deterministic interactions;
the report contains stable command names, counts and action outcomes, never
the prompt, command arguments, query text, result rows, tokens, or secrets.
Reporting runs off the chat path and a telemetry failure does not fail a
successful chat turn. The CLI waits for bounded pending reports on normal
exit. Direct BYOK sessions do not report to the shared AI API.

### Provider routing

`--model`/`--ai <profile>` resolve to one of `strongo/aichat`'s LLM adapters
(`ai/anthropic`, `ai/openaicompat`, or `ai/openairesponses` for the OpenAI
models that only support the Responses API) through `pkg/chat/provider.go`'s
`NewLLMProvider`. A model name is matched, in order, against:

1. an explicit `family/model` routing prefix (`anthropic/`, `openai/`,
   `gemini/`, `mistral/`, `xai/`/`grok/`, `ollama/`, `azure/`, `openrouter/`,
   `opencode/`, `agentgateway/` — `grok/` routes to the `xai` family);
2. Ollama's cloud-tag convention: a bare `:cloud` suffix or the
   `<size>-cloud` form (e.g. `qwen3:cloud`, `deepseek-v3.2:671b-cloud`);
3. a bare model-name prefix (`claude`, `gpt`/`gpt-5`, `gemini`, `mistral`,
   `magistral`, `grok`).

A name matching none of those is an error **unless** `--base-url` is also
given, in which case it is treated as an intentional custom
OpenAI-compatible endpoint (matching family `openai`) instead of silently
defaulting to OpenAI's own API with the wrong credential.

| Family | Default base URL | API key env var(s) | `OPENAI_BASE_URL`-style override |
| --- | --- | --- | --- |
| `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | `GEMINI_API_KEY` | — |
| `mistral` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | — |
| `xai` (and `grok`) | `https://api.x.ai/v1` | `XAI_API_KEY` | — |
| `ollama` | `http://localhost:11434/v1` | (none required) | — |
| `azure` | none (per-deployment; `--base-url` or the env var is required) | `AZUREOPENAI_API_KEY`, then `AZURE_OPENAI_API_KEY`, then `AZURE_API_KEY` (first non-empty wins) | `AZURE_OPENAI_ENDPOINT` |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | — |
| `opencode` | `https://opencode.ai/zen/go/v1` | `OPENCODE_API_KEY` | — |
| `agentgateway` | `http://localhost:4000` | `AGENTGATEWAY_API_KEY` | — |

`gemini`, `mistral`, `xai`, `ollama`, `openrouter`, `opencode`, and
`agentgateway` all speak an OpenAI-compatible wire protocol (`ai/openaicompat`)
through their own endpoint above; `anthropic` speaks its own protocol
(`ai/anthropic`). `--base-url`/an explicit AI profile `baseUrl` always
overrides both the family default and any `*_BASE_URL`/`*_ENDPOINT`
environment fallback. A bare host with no `/v1` path segment anywhere (e.g.
`https://api.deepseek.com`) gets `/v1` appended automatically; a path that
already contains `/v1` (mid-path, for a gateway route, or as a trailing
segment) is left alone.

Responses-only OpenAI models (`gpt-5.6-luna` and the rest of the
`gpt-5.*-codex`/`gpt-5.6-*`/`gpt-6-astra` families — see
`responsesOnlyModelPrefixes` in `pkg/chat/provider.go`) route to
`ai/openairesponses` instead of `ai/openaicompat`, matched after stripping
any routing prefix (so `agentgateway/openai/gpt-6-astra` still resolves on
its bare `gpt-6-astra` ID).

`--thinking low|medium|high` (or an AI profile's `thinking:`) maps to
`ai.ChatRequest.Reasoning`; adapters that don't support a reasoning knob
ignore it rather than failing the call.

OpenAI-compatible providers can also be selected with an explicit base URL.
For example, to use the exact `deepseek-flash` model with a DeepSeek
credential:

```sh
export OPENAI_API_KEY=<your-deepseek-key>

datatug chat \
  --model deepseek-flash \
  --base-url https://api.deepseek.com
```

For repeatable provider setup, define a named AI profile in
`~/.datatug.yaml`. The profile stores provider defaults and, when needed, the
name of the environment variable containing the credential:

```yaml
ai:
  profiles:
    deepseek:
      model: deepseek-flash
      baseUrl: https://api.deepseek.com
      apiKeyEnv: OPENAI_API_KEY
      thinking: low
```

Then select the profile with:

```sh
export OPENAI_API_KEY=<your-deepseek-key>

datatug chat --ai deepseek
```

After a successful start, plain `datatug chat` reuses the last project,
environment, database, AI profile, and access identity. Explicit flags override
those saved choices. Model/base-URL/thinking overrides are remembered only when
explicitly selected, so a profile's updated defaults continue to take effect.
The local settings file is `datatug/chat-last.json` under the OS user config
directory; API keys are never stored there.

The `--model`, `--base-url`, and `--thinking` flags remain available as
explicit per-run overrides. If `--ai` is omitted, the existing default model
and credential behavior are unchanged.

`apiKeyEnv` is optional. When it is omitted, the family's default
environment variable(s) from the table above are checked instead; when it is
set, the named environment variable must contain a non-empty key.

Use `--database` when an environment has more than one catalog. Projects with
access policies must also pass an appropriate `--as`, `--role`, or `--group`,
exactly like other policy-secured DataTug reads.

## Terminal interaction

- Type a question and press Enter. Shift+Enter adds another line without
  sending; the composer grows up to five lines.
- Type `/` at the start of the composer to open a filterable command chooser;
  Up/Down choose and Enter inserts the command for editing or confirmation.
- `/connect` opens a preview of the current connection. Switching the active
  database from this dialog is not implemented yet.
- `/http` or `/http new` opens a request form. `/http post <url>`,
  `/http put <url>`, `/http patch <url>`, and `/http delete <url>` open it
  with method and URL prefilled; the form also offers GET, HEAD and OPTIONS.
  Tab navigates fields, Left/Right changes method, and the header list supports
  Enter to edit and `d` to delete. Add a header through the name/value fields.
  The form starts with applicable configured headers; changes apply only to
  that request. Ctrl+Enter submits, Esc cancels. GET/HEAD reject bodies,
  request bodies are capped at 1 MiB, and non-GET redirects are not followed.
  `/http get <url>` still downloads directly. Responses (up to 16 MiB) enter the
  current session. JSON, CSV and YAML arrays of records open as interactive
  RecordSet tables. The table's `4 Raw` tab shows the original response and
  `5 Headers` shows request headers on the left and response headers/timings on
  the right, stacking them on narrow terminals. Sensitive request and response
  header values are masked in the persisted card. Markdown opens as
  rendered text; Enter or `m` toggles its raw source. Credential-bearing
  response headers are redacted, and URL query strings are not saved. The
  Headers view includes a capped redirect trace (status, sanitized URL and
  per-hop timing) when redirects occurred.
- `/http header` and `/http cookie` list the effective outgoing request
  defaults. Set one with `/http header Name=value` or `/http cookie name=value`;
  DataTug prompts whether to keep it across all CLI projects or only this
  project. Settings stay in a private local database, not project files or AI
  context. Credential-bearing headers and cookies require an HTTP origin;
  specify it as `/http header https://host Name=value` when needed. `User-Agent`
  defaults to `DataTug`.
- `/query <search>` and `/queries <search>` show the same filterable project
  query picker above the composer. Each candidate shows its query type. Up/Down
  navigate and Enter runs the selected saved query via the normal DataTug
  query path; queries with declared parameters first show a small input form.
  The FK picker shows the first 100 permitted rows only. If a key is absent,
  enter it directly in the parameter form; the picker does not search beyond
  that bound yet.
  The query name appears as a user message with its result below. Project HTTP
  queries use the policy-bound saved-query runner; unlike an ad-hoc `/http get`
  card, they currently persist only its structured result, not the raw HTTP
  response and headers.
  On a focused DTQL or HTTP result card, `q` opens the project-query save form:
  enter a name, add multiple tag chips, then save. Request headers and cookies
  are never copied into a project query; HTTP URLs with unsaved parameters
  cannot be captured this way. The request form currently saves GET requests
  without custom headers only; POST/PUT/PATCH/DELETE can be submitted but not
  saved as project queries until project HTTP query execution supports them.
  Project HTTP queries require HTTPS.
  Parameterized DTQL results cannot be captured by `q` yet because this form
  cannot save parameter declarations and bindings; create a project query with
  those declarations instead.
- On a focused query, HTTP table or HTTP document, Ctrl+R refreshes the result.
  Query refresh reuses stored DTQL through DataTug's policy-bound executor,
  without a model call. Refresh creates a new immutable version marked
  `changed` or `unchanged`; earlier versions remain saved. HTTP requests with
  unsaved URL query parameters must be entered again to refresh safely.
  Ctrl+R never repeats a non-GET request; resubmit one explicitly in the form.
  Saved SQL and HTTP project result grids currently rerun through `/query`;
  their cards do not offer Ctrl+R.
- `/settings versions <1-100>` changes how many recent versions of a result
  are shown in history; the default is two. Older snapshots remain stored and
  reappear if the setting is raised. `/settings` shows the current value.
- From an empty input, press Up or Ctrl+G to focus the latest result grid;
  Escape returns to input.
- Up/Down and Page Up/Page Down navigate rows.
- Left/Right choose columns and horizontally window results wider than the terminal.
- Press `s` to sort by the selected column; press it again to reverse the order.
- Enter opens the selected cell's detail dialog with the complete row, column
  metadata, and (when an authoritative foreign key is available) up to five
  related records. The related read uses DTQL and normal DataTug access policy.
  Up/Down or the mouse wheel scrolls the dialog, `y` copies the cell value,
  and Escape returns to the grid.
- `B` adds or removes the focused RecordSet from this session's export bucket;
  `/bucket` lists it and `/bucket clear` empties it. Press `e` on a RecordSet (or
  enter `/export`) to open the export dialog. Choose current RecordSet or bucket,
  format, directory and file name with Tab/Shift+Tab. The directory browser uses
  arrows and Enter; Space chooses its current folder. Escape cancels.
- `/export current <format> <path>` saves the focused RecordSet;
  `/export bucket <format> <path>` saves the bucket. Formats are `csv`, `json`,
  `yaml`, `ingr`, `dbf`, `sqlite`, and `xlsx`. Bucket XLSX has one sheet per
  RecordSet; bucket SQLite has one table per RecordSet; other bucket formats
  produce a ZIP with one file per RecordSet (even for a one-item bucket).
  JSON and YAML store ordered `columns` and `rows` arrays. Existing paths are
  never overwritten. DBF's ten-byte field-name and fixed-width value limits
  mean long column names are shortened and oversized values are rejected.
- The mouse wheel or a touchpad scrolls chat history. Press `F2` to temporarily
  disable mouse capture for normal terminal text selection, then `F2` again to
  restore wheel scrolling. A terminal's mouse-capture override modifier also works.
- `/sessions` lists sessions; `/new` creates one; `/switch <ID-prefix>` reopens
  one; `/rename <title>` renames the current session. `/clear confirm` removes
  its history and cached results, and `/delete confirm` removes the session.
  Run `/help` to see these commands in the terminal.
- On wide terminals, the right-hand workspace has Project, Selected, and Docked
  tabs. `F6` focuses it; Left/Right changes tabs, and Escape returns to input.
  `F3` chooses a configured local DataTug project; switching rebuilds the chat
  runtime and opens that project's own scoped sessions. In Project, Up/Down
  navigates the tree, Enter expands/collapses branches or shows leaf details,
  and Space attaches or detaches an object. In a result grid, Space selects a
  row, `c` a cell, and `r` starts or finishes a row/cell range. The Selected
  workspace tab can attach that Selection explicitly; `d` docks the active
  RecordSet or selection. Attachments appear above the composer and can be
  removed there. On wide terminals, Ctrl+Left/Right resizes the divider.
  On narrow terminals, `F6` toggles the workspace in the available width.

Sessions and result snapshots live in a private SQLite file under
`~/.datatug/chat/`, keyed by the canonical project path. DataTug reopens the
latest session for the selected environment, database, principal, and policy
fingerprint and the current source registry. A different role, changed policy
set, or repointed data source cannot reopen the old scope's cached rows.
Compatible older sessions migrate when their saved source still matches.
Grids restore from saved snapshots without rerunning queries. A first request
gives a new session a short title; `/rename` can
change it at any time.

## Deliberate boundaries

- Schema context comes from the selected catalog's stored, scanned dbmodel. It
  does not independently introspect the live database for every prompt.
- The PoC's `secureread.Result` remains the execution boundary. Phase 2 saves
  immutable copies in session-owned RecordSets with IDs, DTQL, source identity,
  and originating turn. Phase 3 keeps Views, Selections, attachments, and docks
  separate from those immutable results. Bookmarks remain out of scope.
- The agent receives opaque selection references, column names, counts, and
  local parameter names, but not selected row/cell values. DataTug resolves
  selected values locally when it executes a follow-up DTQL query.
- Current DTQL supports one root relation and deliberately rejects joins. A
  request such as "tracks by AC/DC" therefore receives an honest unsupported
  response instead of an SQL fallback. Invoice and Customer scenarios need no
  join and run end to end. Single-table aggregates, such as invoice totals by
  CustomerId, are supported; joining customer names into that result is not.
- Every Chat-generated DTQL query must include a row limit from 1 to 1000. The
  agent defaults to 100 when the user gives no count, and DataTug validates the
  bound before executing the query.
- A turn is limited to 90 seconds and three model calls
  (`agent.Loop.MaxSteps`); tool-call attempts are capped at 12 for the
  terminal chat loop and 2 for the browser-bridge interpret loop (one DTQL
  attempt plus one self-correction — see `pkg/chat/agent.go`'s `newLoop`),
  so a faulty agent loop cannot query or bill indefinitely.
- Applied access-policy limitations remain attached to the structured result
  and are rendered next to the grid, including for empty results.
- Successful data turns display the DataTug-owned grid as the answer. Model
  prose is suppressed on those turns because some small local models expose
  reasoning as ordinary text even when reasoning is disabled.

## strongo/aichat reuse

DataTug's chat path is built entirely on `github.com/strongo/aichat`: its
`ai.LLMProvider` adapters (`ai/anthropic`, `ai/openaicompat`,
`ai/openairesponses`) speak to the model, `ai/agent.Loop` runs the tool-call
loop (see [Provider routing](#provider-routing) above and
`pkg/chat/agent.go`'s `newLoop`/`AskWithContext`/`StreamAskWithContext`), and
`tui/chatshell` + `tui/grid` + `tui/transcript` provide the terminal shell
(focus ring, transcript blocks, overlays, the SidePanel workspace pane, and
the result grid). DataTug owns the DTQL tool (`run_dtql`), the workspace/join
tools, the prompt, session persistence, and all product UI built on top of
chatshell's `SidePanel`/`Overlay`/`GlobalKeys` extension points.

Earlier phases of this PoC evaluated Google ADK
(`google.golang.org/adk`/`genai`) and `github.com/dimetron/pi-go/pimodels`
for the provider/agent seam; both were fully replaced by `strongo/aichat`
and removed from `go.mod` as part of the aichat migration (no residual
import of either remains in the chat path or elsewhere in this module).
