package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/datatug/datatug-cli/pkg/mcpserver"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/spf13/cobra"
)

// agentSessionEnv marks a command run inside an agent session (spec D29).
const agentSessionEnv = "DATATUG_AGENT_SESSION"

const honestAgentsNote = "Local masking is accident prevention for honest agents: on a local connection the agent context is self-declared, so an agent that runs the CLI as you, without --agent, is you and can also edit datatug-labels.json. Only a control plane with a server-verified agent identity guarantees that an agent reaches a protected field only through an elevated approval."

// maskedCell is how a masked cell is shown in text output.
const maskedCell = "••••"

func meaningCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "meaning",
		Short: "Describe what a source's tables and columns mean, read tables with sensitive columns masked, and manage sensitivity labels",
		Long: "Ask the Meaning Graph of a source served by a running `datatug serve` agent: what tables and columns mean, how they join and " +
			"which are sensitive (secret, financial, pii). Labelled columns are masked in reads.\n\n" + honestAgentsNote,
		Args: cobra.NoArgs,
	}
	flags := cmd.PersistentFlags()
	flags.String("agent-url", mcpserver.DefaultAgentURL, "Local DataTug agent origin (http://localhost, 127.0.0.1 or [::1] with an explicit port)")
	flags.Bool("agent", false, "Declare an agent context (also declared when "+agentSessionEnv+" is set). "+honestAgentsNote)
	flags.String("project", "", "Project ID")
	flags.String("env", "", "Environment ID")
	flags.String("source", "", "Source (database) ID")
	flags.String("format", "text", "Output format: text or json")
	cmd.AddCommand(meaningDescribeCommand(), meaningFindCommand(), meaningReadCommand(), meaningLabelCommand())
	return cmd
}

// meaningContext is what every meaning subcommand needs: a client and a scope.
type meaningContext struct {
	client *mcpserver.Client
	scope  mcpserver.MeaningScope
	json   bool
	out    io.Writer
}

func newMeaningContext(cmd *cobra.Command) (*meaningContext, error) {
	flags := cmd.Flags()
	format, _ := flags.GetString("format")
	if format != "text" && format != "json" {
		return nil, fmt.Errorf("--format must be text or json, got %q", format)
	}
	project, _ := flags.GetString("project")
	env, _ := flags.GetString("env")
	source, _ := flags.GetString("source")
	if project == "" || env == "" || source == "" {
		return nil, fmt.Errorf("--project, --env and --source are required")
	}
	address, _ := flags.GetString("agent-url")
	agent, _ := flags.GetBool("agent")
	client, err := mcpserver.NewCommandClient(address, agent || os.Getenv(agentSessionEnv) != "")
	if err != nil {
		return nil, err
	}
	return &meaningContext{client: client, scope: mcpserver.MeaningScope{Project: project, Environment: env, Source: source}, json: format == "json", out: cmd.OutOrStdout()}, nil
}

// runMeaning wraps a subcommand body with context creation and cleanup.
func runMeaning(run func(cmd *cobra.Command, args []string, mc *meaningContext) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		mc, err := newMeaningContext(cmd)
		if err != nil {
			return err
		}
		defer mc.client.Close()
		return run(cmd, args, mc)
	}
}

func (mc *meaningContext) writeJSON(v any) error {
	enc := json.NewEncoder(mc.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func meaningDescribeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "describe", Short: "Describe each table's meaning, columns, effective labels and joins (metadata only, never values)",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: runMeaning(func(cmd *cobra.Command, _ []string, mc *meaningContext) error {
			mc.scope.Tables, _ = cmd.Flags().GetStringArray("table")
			out, err := mc.client.DescribeMeaning(cmd.Context(), mc.scope)
			if err != nil {
				return err
			}
			if mc.json {
				return mc.writeJSON(out)
			}
			writeDescription(mc.out, out)
			return nil
		}),
	}
	cmd.Flags().StringArray("table", nil, "Narrow to this table (repeatable)")
	return cmd
}

func meaningFindCommand() *cobra.Command {
	return &cobra.Command{
		Use: "find <text>", Short: "Find tables and columns whose names, entities or fields match text, with their labels",
		Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: runMeaning(func(cmd *cobra.Command, args []string, mc *meaningContext) error {
			out, err := mc.client.FindMeaning(cmd.Context(), mc.scope, args[0])
			if err != nil {
				return err
			}
			if mc.json {
				return mc.writeJSON(out)
			}
			if len(out.Matches) == 0 {
				_, _ = fmt.Fprintln(mc.out, "No matches.")
			}
			for _, m := range out.Matches {
				target := m.Table
				if m.Column != "" {
					target += "." + m.Column
				}
				line := target
				if m.Type != "" {
					line += " " + m.Type
				}
				if m.Meaning != nil {
					line += " → " + meaningText(*m.Meaning)
				}
				_, _ = fmt.Fprintln(mc.out, line+labelText(m.Label))
			}
			return nil
		}),
	}
}

func meaningReadCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "read", Short: "Read a table through the masking boundary; labelled columns are masked",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: runMeaning(func(cmd *cobra.Command, _ []string, mc *meaningContext) error {
			flags := cmd.Flags()
			schema, _ := flags.GetString("schema")
			table, _ := flags.GetString("table")
			columns, _ := flags.GetStringArray("column")
			limit, _ := flags.GetInt("limit")
			reveal, _ := flags.GetStringArray("reveal")
			if all, _ := flags.GetBool("reveal-all"); all {
				reveal = []string{"*"}
			}
			out, err := mc.client.ReadTable(cmd.Context(), mcpserver.TableReadRequest{Project: mc.scope.Project, Environment: mc.scope.Environment, Source: mc.scope.Source, Schema: schema, Table: table, Columns: columns, Limit: limit, Reveal: reveal})
			if err != nil {
				return err
			}
			if mc.json {
				return mc.writeJSON(out)
			}
			writeTableRead(mc.out, out)
			return nil
		}),
	}
	cmd.Flags().String("schema", "", "Schema name; empty for SQLite")
	cmd.Flags().String("table", "", "Table name")
	cmd.Flags().StringArray("column", nil, "Read only this column (repeatable)")
	cmd.Flags().Int("limit", 0, "Returned row limit from 1 to 500; default 100")
	cmd.Flags().StringArray("reveal", nil, "Reveal this revealable column for this read only (repeatable; people only, refused for an agent, never a non-revealable column; recorded in the local audit log)")
	cmd.Flags().Bool("reveal-all", false, "Reveal every revealable column for this read only (people only; see --reveal)")
	_ = cmd.MarkFlagRequired("table")
	return cmd
}

func meaningLabelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "List and change sensitivity labels (set, confirm, dismiss, clear and class are owner actions; agents may only propose)",
		Long:  "List and change sensitivity labels. Every change names a reason. " + honestAgentsNote,
		Args:  cobra.NoArgs,
	}
	list := &cobra.Command{
		Use: "list", Short: "List every effective label with its origin, state and rule, suggestions included",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: runMeaning(func(cmd *cobra.Command, _ []string, mc *meaningContext) error {
			table, _ := cmd.Flags().GetString("table")
			if table != "" {
				mc.scope.Tables = []string{table}
			}
			out, err := mc.client.MeaningLabels(cmd.Context(), mc.scope)
			if err != nil {
				return err
			}
			if mc.json {
				return mc.writeJSON(out)
			}
			writeLabels(mc.out, out)
			return nil
		}),
	}
	list.Flags().String("table", "", "Narrow to this table")
	cmd.AddCommand(list)
	for _, action := range []struct {
		action meaninglabels.Action
		short  string
	}{
		{meaninglabels.ActionSet, "Set an owner label on a table or column (class secret, financial, pii or none)"},
		{meaninglabels.ActionConfirm, "Confirm a column's current suggestion or proposal as an owner label"},
		{meaninglabels.ActionDismiss, "Dismiss a column's current suggestion or proposal"},
		{meaninglabels.ActionClear, "Clear an owner label"},
		{meaninglabels.ActionPropose, "Propose a label; it is pending and masks the field until an owner decides (the only action allowed to agents)"},
		{meaninglabels.ActionClassMark, "Set the source-wide policy for a class (--class, --non-revealable)"},
	} {
		cmd.AddCommand(meaningLabelChangeCommand(action.action, action.short))
	}
	return cmd
}

func meaningLabelChangeCommand(action meaninglabels.Action, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use: string(action), Short: short,
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: runMeaning(func(cmd *cobra.Command, _ []string, mc *meaningContext) error {
			flags := cmd.Flags()
			table, _ := flags.GetString("table")
			column, _ := flags.GetString("column")
			class, _ := flags.GetString("class")
			kind, _ := flags.GetString("kind")
			nonRevealable, _ := flags.GetBool("non-revealable")
			reason, _ := flags.GetString("reason")
			if action != meaninglabels.ActionClassMark && table == "" {
				return fmt.Errorf("--table is required")
			}
			change := meaninglabels.Change{Action: action, Target: meaninglabels.Target{Source: mc.scope.Source, Table: table, Column: column},
				Class: meaninglabels.Class(class), Kind: meaninglabels.Kind(kind), NonRevealable: nonRevealable, Reason: reason}
			if table != "" {
				mc.scope.Tables = []string{table}
			}
			current, err := mc.client.MeaningLabels(cmd.Context(), mc.scope)
			if err != nil {
				return err
			}
			if (action == meaninglabels.ActionConfirm || action == meaninglabels.ActionDismiss) && change.Class == "" {
				// Confirming or dismissing a read-time suggestion names its class.
				if label := findLabel(current, table, column); label != nil {
					change.Class, change.Kind = meaninglabels.Class(label.Class), meaninglabels.Kind(label.Kind)
				}
			}
			out, err := mc.client.ChangeLabel(cmd.Context(), mc.scope, current.Revision, change)
			if err != nil {
				return err
			}
			if mc.json {
				return mc.writeJSON(out)
			}
			target := change.Target.String()
			if table == "" {
				target = mc.scope.Source
			}
			_, _ = fmt.Fprintf(mc.out, "%s %s; labels revision %s\n", out.Event.Type, target, out.Revision)
			return nil
		}),
	}
	flags := cmd.Flags()
	if action != meaninglabels.ActionClassMark {
		flags.String("table", "", "Table name, exactly as the source reports it")
		flags.String("column", "", "Column name; omit to target the whole table")
	}
	flags.String("class", "", "Class: secret, financial, pii (or none for set)")
	flags.String("kind", "", "Kind within the class, such as email or card-number")
	flags.Bool("non-revealable", false, "Mark non-revealable: never opened by any approval")
	flags.String("reason", "", "Why the change is made (recorded in the audit log)")
	return cmd
}

func findLabel(list mcpserver.LabelsList, table, column string) *mcpserver.EffectiveLabel {
	for _, t := range list.Tables {
		if t.Name != table && t.Schema+"."+t.Name != table {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == column && c.Label != nil {
				return c.Label
			}
		}
	}
	return nil
}

func labelText(l *mcpserver.EffectiveLabel) string {
	if l == nil || l.Class == "" {
		return ""
	}
	class := l.Class
	if l.Kind != "" {
		class += "/" + l.Kind
	}
	details := []string{}
	for _, part := range []string{l.Origin, l.State} {
		if part != "" {
			details = append(details, part)
		}
	}
	if l.Rule != "" {
		details = append(details, "rule "+l.Rule)
	}
	if l.NonRevealable {
		details = append(details, "non-revealable")
	}
	if len(details) == 0 {
		return " [" + class + "]"
	}
	return " [" + class + ": " + strings.Join(details, ", ") + "]"
}

func meaningText(m mcpserver.FieldMeaning) string {
	text := m.Entity
	if m.Field != "" {
		text += "." + m.Field
	}
	if m.Title != "" && m.Title != m.Entity && m.Title != m.Field {
		text += " (" + m.Title + ")"
	}
	if m.Provenance != "" {
		text += " [" + m.Provenance + "]"
	}
	if m.Description != "" {
		text += ": " + m.Description
	}
	return text
}

func meaningQualified(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

func writeDescription(w io.Writer, d mcpserver.MeaningDescription) {
	for i, t := range d.Tables {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		header := meaningQualified(t.Schema, t.Name) + " (" + t.Kind + ")"
		if !t.Reviewed {
			header += " unreviewed"
		}
		_, _ = fmt.Fprintln(w, header)
		for _, m := range t.Meanings {
			_, _ = fmt.Fprintln(w, "  means "+meaningText(m))
		}
		keys := map[string]string{}
		for _, pk := range t.PrimaryKey {
			keys[pk] = " PK"
		}
		for _, fk := range t.ForeignKeys {
			for _, c := range fk.Columns {
				keys[c] += " FK→" + meaningQualified(fk.RefTable.Schema, fk.RefTable.Name)
			}
		}
		for _, c := range t.Columns {
			line := "  " + c.Name + " " + c.Type
			if c.Nullable != nil && !*c.Nullable {
				line += " NOT NULL"
			}
			line += keys[c.Name]
			if c.Meaning != nil {
				line += " → " + meaningText(*c.Meaning)
			}
			_, _ = fmt.Fprintln(w, line+labelText(c.Label))
		}
	}
	if len(d.Joins) > 0 {
		_, _ = fmt.Fprintln(w, "\nJoins:")
		for _, j := range d.Joins {
			_, _ = fmt.Fprintf(w, "  %s(%s) → %s(%s) [%s]\n", j.From.Table, strings.Join(j.From.Columns, ", "), j.To.Table, strings.Join(j.To.Columns, ", "), j.Provenance)
		}
	}
	if d.Notice != "" {
		_, _ = fmt.Fprintln(w, "\n"+d.Notice)
	}
}

// meaningCellText renders one raw cell: a typed value {"type","value"}, a bare JSON
// scalar or null. Masked columns always show maskedCell.
func meaningCellText(raw json.RawMessage, masked bool) string {
	if masked {
		return maskedCell
	}
	var typed struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	}
	if json.Unmarshal(raw, &typed) == nil && typed.Type != "" {
		if typed.Type == "null" || typed.Value == nil {
			return "NULL"
		}
		return fmt.Sprint(typed.Value)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "NULL"
	}
	return fmt.Sprint(value)
}

func writeTableRead(w io.Writer, r mcpserver.TableRead) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	masked := make([]bool, len(r.Recordset.Columns))
	headers := make([]string, len(r.Recordset.Columns))
	for i, c := range r.Recordset.Columns {
		_, masked[i] = r.Protection[c.Name]
		headers[i] = c.Name
	}
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range r.Recordset.Rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = meaningCellText(cell, masked[i])
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
	if r.Truncated {
		_, _ = fmt.Fprintln(w, "(truncated)")
	}
	for _, c := range r.Recordset.Columns {
		p, ok := r.Protection[c.Name]
		if !ok {
			continue
		}
		class := string(p.Class)
		if p.Kind != "" {
			class += "/" + string(p.Kind)
		}
		revealable := "non-revealable"
		if p.Revealable {
			revealable = "revealable"
		}
		line := fmt.Sprintf("%s: %s %s (%s", c.Name, class, p.State, revealable)
		if p.Origin != "" {
			line += ", " + p.Origin
		}
		line += ")"
		if p.Ask != "" {
			line += " — " + p.Ask
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if r.Notice != "" {
		_, _ = fmt.Fprintln(w, r.Notice)
	}
}

func writeLabels(w io.Writer, l mcpserver.LabelsList) {
	_, _ = fmt.Fprintf(w, "Labels revision: %s\n", l.Revision)
	for _, t := range l.Tables {
		header := meaningQualified(t.Schema, t.Name) + " (" + t.Kind + ")"
		if !t.Reviewed {
			header += " unreviewed"
		}
		_, _ = fmt.Fprintln(w, header)
		for _, c := range t.Columns {
			label := labelText(c.Label)
			if label == "" {
				label = " -"
			}
			_, _ = fmt.Fprintln(w, "  "+c.Name+" "+c.Type+label)
		}
	}
}
