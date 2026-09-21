Chat command in datatug-cli

DataTug Chat PoC — AI → DTQL → Interactive Grid

Implement the first proof of concept for datatug chat.

The purpose of this phase is deliberately narrow: prove that a user can ask a simple natural-language question about real data, an inexpensive AI model can translate the request into DTQL, DataTug can execute that DTQL using its existing data/query infrastructure, and the result can be displayed as a useful interactive grid directly inside the terminal chat.

Do not build the complete DataTug Chat vision yet. Get this vertical slice working cleanly, test it with real data, review it, and merge it to main once the PoC is solid.

1. Target experience

The user should be able to run something along the lines of:

datatug chat

against a DataTug project configured with the Chinook sample database and type:

> Show last 100 orders

DataTug should:

1. send the user’s request plus the necessary project/schema context to the AI agent;
2. have the agent produce DTQL rather than SQL or formatted table output;
3. validate and execute the DTQL through normal DataTug query infrastructure;
4. receive a structured result/RecordSet;
5. render that result as an inline interactive terminal grid inside the chat.

Another canonical scenario:

> Show 50 customers from Prague

The result must come from the actual Chinook database.

Do not hard-code these prompts, SQL, DTQL, table names, filters, or results.

The point of the PoC is to demonstrate the generic pipeline.

2. Architecture to prove

The vertical slice should be:

User
  ↓
DataTug Chat
  ↓
AI agent
  ↓
DTQL
  ↓
DTQL validation/execution
  ↓
existing DataTug data-source/query infrastructure
  ↓
real database
  ↓
structured RecordSet/result
  ↓
interactive inline grid

Maintain this separation.

In particular:

* the LLM must not produce an ASCII/Markdown table;
* the LLM should not generate SQL when DTQL is the appropriate DataTug abstraction;
* the TUI should not parse textual model output to discover table data;
* query execution belongs to DataTug;
* grid rendering belongs to DataTug;
* the model’s responsibility is primarily understanding intent and producing an appropriate structured action/DTQL.

3. Start by inspecting what already exists

Before designing or implementing anything, inspect the current DataTug repository carefully.

Understand:

* CLI architecture and conventions;
* current DTQL implementation;
* existing query execution paths;
* project/data-source configuration;
* database/schema discovery;
* existing result/record structures;
* existing TUI dependencies/components;
* tests and test conventions.

Reuse existing DataTug abstractions wherever they are appropriate.

Do not build a parallel query engine just for Chat.

4. Investigate pi-go as reference/source material

We want to explore using pi-go as a starting point/reference for the chat harness.

Inspect its current architecture and determine which parts are useful for this PoC, particularly:

* Google ADK integration;
* model/provider integration;
* agent loop;
* streaming;
* Bubble Tea/TUI architecture;
* chat history/rendering;
* input handling;
* tool execution.

pi-go is MIT licensed. Reusing or adapting useful code is acceptable, provided all required attribution/license obligations are preserved.

Do not make DataTug unnecessarily dependent on pi-go as an architectural layer.

Prefer:

DataTug Chat
    ├── DataTug-owned UI/domain behaviour
    ├── Google ADK / model abstraction
    └── selected reusable/adapted pi-go ideas/code

rather than:

DataTug
    └── pi-go application
         └── DataTug modifications

Do not rewrite working pi-go functionality merely to avoid an MIT dependency. Equally, do not import large amounts of coding-agent functionality DataTug does not need.

At the end, report what pi-go code/ideas were reused and any attribution requirements.

5. AI model

The runtime model for this PoC should be fast and inexpensive.

Start with a small model such as GPT-5.6 Luna, Claude Haiku, or DeepSeek Flash according to what integrates most naturally with the existing/ADK architecture.

Prefer Luna initially if there is no strong implementation reason otherwise.

Do not use an expensive frontier model as the default runtime model for translating straightforward requests into DTQL.

However, avoid tightly coupling DataTug Chat to one provider/model. Model selection should be configurable enough that we can cheaply compare Luna, Haiku and DeepSeek later.

Do not build an elaborate provider framework if ADK/pi-go already provides the necessary abstraction.

6. Agent behaviour

Keep the agent’s responsibility constrained.

For this PoC it should essentially perform:

natural-language request
        +
relevant DataTug project/schema context
        ↓
understand intent
        ↓
produce valid DTQL / invoke appropriate DataTug query tool

DataTug then validates and executes the request.

The model should receive enough schema/project information to resolve requests correctly, but do not blindly dump an entire large project/database schema into every prompt if existing tooling can expose/query schema information more efficiently.

Design this minimally for Chinook while avoiding assumptions that only work for Chinook.

7. RecordSet boundary

Even though richer RecordSet functionality comes later, establish the correct boundary now.

Query execution should produce a structured result that the UI consumes.

Conceptually:

DTQL
  ↓
execute
  ↓
RecordSet
  ├── columns/schema
  ├── rows
  └── basic execution/query metadata
        ↓
Grid

Use existing DataTug result abstractions if they already provide this.

Do not prematurely implement the complete future RecordSet persistence/domain model.

The important invariant for this phase is simply:

Data is structured before it reaches the UI.

8. Chat UI

For the PoC, keep the UI small.

We need:

┌─────────────────────────────────────────────────┐
│                                                 │
│              scrollable chat history            │
│                                                 │
│ User: Show last 100 orders                      │
│                                                 │
│ ┌─────────────────────────────────────────────┐ │
│ │ ID │ Customer │ Date │ Total │ ...         │ │
│ │ ...                                         │ │
│ └─────────────────────────────────────────────┘ │
│                                                 │
├─────────────────────────────────────────────────┤
│ > Ask about your data...                        │
└─────────────────────────────────────────────────┘

The input remains at the bottom, as expected for an AI chat.

Agent text and query-result grids appear in the scrollable history above it.

Do not implement the final split-screen workspace yet.

9. Grid

The result must be a real grid component, not preformatted text.

For this PoC, implement only enough interaction to prove the concept well:

* keyboard navigation;
* vertical scrolling;
* sensible handling of more columns than available terminal width;
* column headers;
* basic value formatting;
* preferably simple column sorting if cheap and natural with the chosen component.

Investigate existing Go/Bubble Tea grid/table components before implementing one from scratch.

Choose something we can reasonably extend later.

Do not yet implement:

* complex filtering;
* cell ranges;
* multiple selections;
* named selections/views;
* docking;
* bookmarking;
* tags.

10. Real Chinook scenario

Use a real Chinook database as the initial deterministic test/demo source.

Canonical prompts:

Show last 100 orders

and:

Show 50 customers from Prague

Also test variations such as:

Show the last 20 invoices
Show customers from Brazil
Show the newest 30 invoices
Show 10 tracks by AC/DC

The exact wording should not matter.

Validate the generated DTQL and returned rows against deterministic database queries where practical.

If Chinook terminology differs from “orders” (for example invoices represent purchases), the agent should resolve the user’s ordinary-language intent through schema understanding rather than requiring database-specific wording.

11. Testing

Add focused automated tests around boundaries that can be tested deterministically.

At minimum test:

* model/tool response → DTQL handling;
* DTQL validation/execution;
* result → RecordSet conversion;
* RecordSet → grid model;
* malformed/invalid agent output;
* query execution errors;
* empty results.

Do not make unit tests depend unnecessarily on live paid model calls.

Where useful, provide a fake/model stub so the pipeline can be tested deterministically.

Also perform real manual/integration runs using the configured inexpensive model and Chinook.

12. Error UX

Failures must remain understandable.

Examples:

I couldn't construct a valid query for that request.

or:

Query failed: column "foo" does not exist.

Do not dump raw Go errors or huge model/tool payloads into the normal chat UI.

Detailed diagnostics can go to debug logging.

13. Explicitly out of scope

We have a larger direction for DataTug Chat, including:

* multiple persistent chat sessions;
* durable session RecordSet cache;
* restart/context restoration;
* RecordSet lineage;
* views and selections;
* row/cell-range selection;
* named semantic selections;
* docking;
* right-side workspace;
* project/database explorer;
* attaching tables/views/queries to chat context;
* bookmarked RecordSets;
* bookmark tags;
* cross-session bookmarks;
* charts and other artifact types.

These are important future requirements, but they are context only for this phase.

Do not implement them now.

Avoid decisions that obviously make them difficult later, but do not create speculative frameworks for them either.

The immediate objective is learning from a working PoC.

14. Implementation approach

Work autonomously, but incrementally.

First inspect DataTug, pi-go and relevant ADK/TUI libraries.

Then write a short implementation plan based on the actual code rather than assumptions.

Prefer the smallest coherent architecture capable of proving the vertical slice.

Implement progressively so the system remains runnable throughout development.

Use existing DataTug conventions and abstractions.

Avoid broad unrelated refactoring.

Do not duplicate functionality already present in DataTug.

After implementation:

1. run unit tests;
2. run relevant existing DataTug tests;
3. exercise the real Chinook scenarios;
4. inspect the actual terminal UX;
5. review the implementation for unnecessary complexity;
6. review pi-go-derived code/licensing;
7. fix issues discovered during review.

15. Definition of success

This phase succeeds when I can run the normal DataTug CLI and experience something equivalent to:

$ datatug chat
> Show last 100 orders
[interactive grid containing the real results]
> Show 50 customers from Prague
[interactive grid containing the real results]

and:

* a cheap/fast AI model actually interprets the request;
* it produces DTQL through the intended agent/tool architecture;
* DataTug validates and executes the DTQL;
* existing DataTug data access/query infrastructure is reused;
* results come from a real Chinook database;
* results remain structured throughout the pipeline;
* the chat renders them as an interactive inline grid;
* differently worded simple queries work without hard-coded intent handling;
* normal text/error responses render correctly;
* the implementation has focused automated coverage;
* the architecture is small and understandable;
* there is no unnecessary dependency on pi-go’s coding-agent functionality.

Once these criteria are met, clean up the implementation and merge this PoC to main.

Do not proceed into the larger DataTug Chat roadmap in this task. We will use the working PoC, learn from it, and define the next phase separately.
