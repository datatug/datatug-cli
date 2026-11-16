package secureread

import "github.com/datatug/datatug-core/pkg/datatug"

// refuseContextVariables refuses a structured TugQL document that uses a
// context variable ("{SneatUserID}" in a source path, or a SneatUserID
// parameter reference) with datatug-core's ContextVariableNotBoundError
// (errors.Is datatug.ErrContextVariableNotBound). Only the DataTug web app
// binds context variables, so every TugQL entry point of the Executor calls
// it before a source opens: a backstop for callers that run TugQL without a
// saved QueryDef of their own to check.
func refuseContextVariables(document []byte) error {
	return datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: string(document), StructuredBody: true}.RefuseContextVariables()
}
