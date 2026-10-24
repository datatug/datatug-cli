# Design review r1: MCP Apps adapter

Pinned artifact: spec/ideas/mcp-apps-adapter.md SHA256 03bb22872c0bd5905047b8d1d8c37501207086e7e501d9caf59fd437dda7bbec; personally verified. Source bases supplied and inspected: CLI428847f1f962e56475e72ffb8cd6e79cbed5289d; ai-plugin41a2c10252c490da4788edac075866c88bca0b43. Root owns landing.

No blockers or majors found. Two minors remain open on these exact bytes:

1. Precision/reuse ambiguity (lines40,55,82). Design mandates exact upstream helper reuse while promising precise numeric behavior. ai-plugin/lib/rows.js15-19 casts noninteger decimals to Number: personally ran sortedOrder on 9007199254740992.2 followed by 9007199254740992.1 ascending and obtained [0,1], preserving wrong numeric order. Require upstream comparator fix and refresh, or new exact comparator while reusing safe upstream helpers. Explicitly retain typed integer/decimal string values across JSON parsing, sorting, display and copy. The resolved current core v0.77.0 (CLI go.mod40), personally checked, TypedValue MarshalJSON44-51 string-tags integers/decimals; no wire-format replacement is needed. Disposition: author notified; pending.
2. Single-line header ambiguity (line39). Separate tabs below header constitute another row of view chrome, despite relabeling them navigation. User explicitly requires only one line in page/content header. Put title, view selector and compact actions in the single toolbar; use compact select/menu on narrow screens. Disposition: author notified; pending.

Scope reviewed: actual current MCP generic AddTool output, saved execution envelope, Mod pure helpers and MIT license, GoSDK1.8.0 Extensions capabilities, official stable MCP Apps specification. Design avoids raw-row alternate tool on Apps connection, has explicit model-safe envelope, session-scoped bounded capabilities, fresh authorization checks, Go embedded offline assets and official SDK handshake acceptance gates.

Verification limits: read-only design review; no compilation, integration, real-host compatibility, CSP or privacy success claimed. One Node pure-helper reproducer only. No implementation reviewed. No private settings changed. No commits or source edits.

VERDICT: blockers=0 majors=0 minors=2 land=no
