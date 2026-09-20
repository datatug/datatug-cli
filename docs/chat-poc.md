# DataTug Chat PoC

This phase proves one vertical slice:

```text
user -> ADK agent -> run_dtql tool -> secureread.Executor -> DALgo backend
     -> secureread.Result -> Bubbles table
```

The model never executes SQL and never renders rows. Its only data tool accepts
a DTQL YAML document. DataTug validates that document with `dtql.Deserialize`,
applies the normal access-policy path, executes it through the configured DALgo
adapter, and gives the Bubble Tea UI a structured `secureread.Result`.

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
translation task. DataTug passes the effort both to pi-go's provider resolver
and through ADK's portable generation configuration; exact supported levels
remain provider-specific. `OPENAI_API_KEY` must be present. Model selection
stays configurable through pi-go's provider resolver, for example:

```sh
datatug chat --model ollama/qwen3:4b
```

OpenAI-compatible providers can be selected with an explicit base URL. For
example, to use the exact `deepseek-flash` model with a DeepSeek credential
already stored by the pi harness:

```sh
OPENAI_API_KEY="$(pi auth print-api-key --provider deepseek)" \
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
      apiKeyEnv: DEEPSEEK_API_KEY
      thinking: low
```

Then select the profile with:

```sh
DEEPSEEK_API_KEY="$(pi auth print-api-key --provider deepseek)" \
datatug chat --ai deepseek
```

The `--model`, `--base-url`, and `--thinking` flags remain available as
explicit per-run overrides. If `--ai` is omitted, the existing default model
and credential behavior are unchanged.

`apiKeyEnv` is optional. When it is omitted, pi-go/provider environment,
OAuth, or keyless authentication behavior is used instead; when it is set,
the named environment variable must contain a non-empty key.

Use `--database` when an environment has more than one catalog. Projects with
access policies must also pass an appropriate `--as`, `--role`, or `--group`,
exactly like other policy-secured DataTug reads.

## Terminal interaction

- Type a question and press Enter.
- From an empty input, press Up or Ctrl+G to focus the latest result grid;
  Escape returns to input.
- Up/Down and Page Up/Page Down navigate rows.
- Left/Right choose columns and horizontally window results wider than the terminal.
- Enter sorts by the selected column; press it again to reverse the order.
- Bubble Tea mouse capture stays disabled so the terminal can select and copy
  text normally.

## Deliberate boundaries

- Schema context comes from the selected catalog's stored, scanned dbmodel. It
  does not independently introspect the live database for every prompt.
- The PoC keeps the existing `secureread.Result` as its RecordSet boundary; it
  does not add persistence, lineage, selections, bookmarks, or a workspace.
- Current DTQL supports one root relation and deliberately rejects joins. A
  request such as "tracks by AC/DC" therefore receives an honest unsupported
  response instead of an SQL fallback. Invoice and Customer scenarios need no
  join and run end to end.
- Every Chat-generated DTQL query must include a row limit from 1 to 1000. The
  agent defaults to 100 when the user gives no count, and DataTug validates the
  bound before executing the query.
- A turn is limited to 90 seconds, three model calls, and two DTQL tool
  attempts (the initial query plus one correction), so a faulty agent loop
  cannot query or bill indefinitely.
- Applied access-policy limitations remain attached to the structured result
  and are rendered next to the grid, including for empty results.
- Successful data turns display the DataTug-owned grid as the answer. Model
  prose is suppressed on those turns because some small local models expose
  reasoning as ordinary text even when reasoning is disabled.

## pi-go reuse

DataTug imports only `github.com/dimetron/pi-go/pimodels`. That package resolves
configurable model/provider names and returns Google ADK's `model.LLM`
interface. DataTug owns the ADK agent, DTQL tool, prompt, session behavior, UI,
and query execution. No pi-go coding-agent, filesystem tools, shell tools,
skills, memory, or session persistence are embedded.

The implementation also follows pi-go's useful architectural ideas: meet at
the ADK `model.LLM` seam, keep providers separate from the agent, use a fake
model for deterministic tool-loop tests, and keep the chat input fixed below a
scrollable Bubble Tea history. No pi-go source was copied or adapted.

pi-go is MIT licensed. Its copyright and permission notice are retained in
`THIRD_PARTY_NOTICES.md`; that notice must accompany distributions containing
the dependency.
