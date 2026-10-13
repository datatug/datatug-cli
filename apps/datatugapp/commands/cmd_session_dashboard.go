package commands

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

const sessionChinookHash = "7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15"
const sessionChinookURL = "https://chinook.demodb.dev/data/chinook.sqlite"
const sessionInvoiceDashboard = "chinook-invoice-overview"
const sessionInvoiceDashboardRevision = "chinook-invoice-overview-1"

var sessionRunInvoiceDashboard = runSessionInvoiceDashboard

const sessionInvoiceLinesSQL = `SELECT il.InvoiceLineId,il.TrackId,t.Name,g.Name AS Genre,il.Quantity,il.UnitPrice,ROUND(il.Quantity*il.UnitPrice,2) AS Total FROM InvoiceLine il JOIN Track t ON t.TrackId=il.TrackId LEFT JOIN Genre g ON g.GenreId=t.GenreId WHERE il.InvoiceId=? ORDER BY il.InvoiceLineId`
const sessionInvoiceGenreSQL = `SELECT COALESCE(g.Name,'Unknown') AS Genre,ROUND(SUM(il.Quantity*il.UnitPrice),2) AS Total FROM InvoiceLine il JOIN Track t ON t.TrackId=il.TrackId LEFT JOIN Genre g ON g.GenreId=t.GenreId WHERE il.InvoiceId=? GROUP BY g.GenreId,g.Name ORDER BY Total DESC,Genre`

type sessionDashboardResult struct {
	Grid          apicontract.Recordset `json:"grid"`
	Chart         apicontract.Recordset `json:"chart"`
	FixtureSHA256 string                `json:"fixtureSha256"`
	OutcomeOrigin string                `json:"outcomeOrigin"`
}

// The sample runs locally against the same admitted immutable public SQLite
// fixture as the browser. Its outcome is explicitly client-reported, including
// when working context lives in the hosted private session authority.
func sessionDashboardCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "dashboard <session-id>", Short: "Run the admitted Chinook invoice grid and genre chart with one pinned input", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		scope, err := cli.scope(cmd)
		if err != nil {
			return err
		}
		var admitted *sessioncontext.Subject
		for _, subject := range cli.caps.Subjects {
			if subject.Scope == *scope && subject.ID == "chinook-invoice-id" && subject.Type == apicontract.ValueTypeInteger && subject.Shape == "scalar" && subject.Subject.ModelRevision == "19be3e0f575e14aa999769da79ba3ecb7fe3cb7e6a7dee151399dcbebfc56a7b" && subject.Subject.GraphRevision == "08070167da1626c3fd51f6555e846cd8aa1aa068a533db45c53394127bb0fcad" {
				value := subject.Subject
				admitted = &value
			}
		}
		if admitted == nil {
			return errors.New("this project has no admitted Chinook invoice dashboard source")
		}
		op := sessionOp(cmd)
		if recovered, recoveryErr := cli.recoverClientRun(cmd, op, args[0], "dashboard"); recovered || recoveryErr != nil {
			return recoveryErr
		}
		snapshot, err := cli.snapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		invoiceBeforePin, preflightErr := cli.appliedInvoice(cmd.Context(), snapshot, *scope, *admitted)
		if preflightErr != nil {
			return preflightErr
		}
		pin := sessioncontext.Command{DefinitionDocument: sessionInvoiceDefinitionDocument(), ResolvedParameters: map[string]apicontract.TypedValue{"InvoiceId": invoiceBeforePin}, OpID: op, Action: "pinInput", Kind: "dashboard", SessionID: args[0], ExpectedVersion: snapshot.Session.Version, ExpectedContextSetID: snapshot.ContextSet.ID, DatabaseID: cli.caps.DatabaseID, ProjectID: cli.caps.ProjectID, Scope: scope, InputSnapshotID: uuid.NewString(), RunID: uuid.NewString(), DefinitionID: sessionInvoiceDashboard, DefinitionRevision: sessionInvoiceDashboardRevision}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "operation %s\n", op)
		var receipt sessioncontext.CommandResult
		if _, err = cli.request(cmd.Context(), http.MethodPost, "/commands", pin, &receipt); err != nil {
			return fmt.Errorf("%w; recover operation %s before retrying", err, op)
		}
		var input struct {
			Input     sessioncontext.InputSnapshot   `json:"input"`
			Revisions []sessioncontext.EntryRevision `json:"revisions"`
		}
		if _, err = cli.request(cmd.Context(), http.MethodGet, "/inputs/"+pin.InputSnapshotID, nil, &input); err != nil {
			return err
		}
		var invoice *apicontract.TypedValue
		for _, revision := range input.Revisions {
			if reflect.DeepEqual(revision.Subject, *admitted) && revision.Value.Kind == "scalar" && revision.Value.Scalar != nil && revision.Value.Scalar.Type == apicontract.ValueTypeInteger {
				if invoice != nil {
					return errors.New("dashboard has ambiguous invoice inputs")
				}
				invoice = revision.Value.Scalar
			}
		}
		if invoice == nil {
			return errors.New("apply one admitted scalar Invoice ID before Run")
		}
		if pinnedParameter, exists := input.Input.ResolvedParameters["InvoiceId"]; !exists || pinnedParameter != *invoice {
			return errors.New("pinned dashboard parameters do not match its immutable invoice reference")
		}
		fixture, _ := cmd.Flags().GetString("fixture")
		result, executionErr := sessionRunInvoiceDashboard(cmd.Context(), fixture, invoice.Str)
		cacheAvailable := false
		if executionErr == nil {
			cacheAvailable = cli.cacheDashboardDisplay(cmd.Context(), pin.RunID, result) == nil
		}
		reportDigest := sha256.Sum256([]byte(op))
		report := sessioncontext.Command{OpID: "report-" + hex.EncodeToString(reportDigest[:]), Action: "reportDashboard", SessionID: args[0], RunID: pin.RunID, ExpectedRunVersion: 1, RunState: "completed", DatabaseID: cli.caps.DatabaseID, ProjectID: cli.caps.ProjectID}
		if executionErr != nil {
			report.RunState = "failed"
			report.ErrorCode = "SOURCE_UNAVAILABLE"
		} else {
			report.Widgets = dashboardWidgetOutcomes(result)
		}
		var reported sessioncontext.CommandResult
		if _, err = cli.request(cmd.Context(), http.MethodPost, "/commands", report, &reported); err != nil {
			return fmt.Errorf("dashboard outcome report acknowledgement unavailable; inspect session operation %s and session run %s; original dashboard operation %s", report.OpID, pin.RunID, pin.OpID)
		}
		if executionErr != nil {
			return executionErr
		}
		cacheAvailability := "unavailable"
		if cacheAvailable {
			cacheAvailability = "available"
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Receipt             sessioncontext.Receipt `json:"receipt"`
			RunID               string                 `json:"runId"`
			InputSnapshotID     string                 `json:"inputSnapshotId"`
			Result              sessionDashboardResult `json:"result"`
			DisplayStorage      string                 `json:"displayStorage"`
			DisplayAvailability string                 `json:"displayAvailability"`
		}{reported.Receipt, pin.RunID, pin.InputSnapshotID, result, "local-device", cacheAvailability})
	}}
	cmd.Flags().String("fixture", "", "Existing exact pinned Chinook SQLite file (otherwise fetch the admitted public fixture)")
	return cmd
}

func runSessionInvoiceDashboard(ctx context.Context, path, invoice string) (sessionDashboardResult, error) {
	var result sessionDashboardResult
	invoiceID, err := strconv.ParseInt(invoice, 10, 64)
	if err != nil || invoiceID < 1 {
		return result, errors.New("dashboard requires a positive integer Invoice ID")
	}
	if path == "" {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, sessionChinookURL, nil)
		if requestErr != nil {
			return result, requestErr
		}
		client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, fetchErr := client.Do(req)
		if fetchErr != nil {
			return result, errors.New("public sample source unavailable")
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return result, errors.New("public sample source unavailable")
		}
		f, createErr := os.CreateTemp("", "datatug-session-chinook-*.sqlite")
		if createErr != nil {
			return result, errors.New("private sample download unavailable")
		}
		path = f.Name()
		downloadedPath := path
		defer func() { _ = os.Remove(downloadedPath) }()
		_, copyErr := io.Copy(f, io.LimitReader(response.Body, 1007617))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return result, errors.New("public sample download failed")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return result, errors.New("sample fixture unavailable")
	}
	snapshot, createErr := os.CreateTemp("", "datatug-session-pinned-*.sqlite")
	if createErr != nil {
		_ = f.Close()
		return result, errors.New("private fixture snapshot unavailable")
	}
	defer func() { _ = os.Remove(snapshot.Name()) }()
	hash := sha256.New()
	size, hashErr := io.Copy(io.MultiWriter(hash, snapshot), io.LimitReader(f, 1007617))
	_ = f.Close()
	snapshotCloseErr := snapshot.Close()
	if snapshotCloseErr != nil {
		return result, errors.New("private fixture snapshot unavailable")
	}
	path = snapshot.Name()
	if hashErr != nil || size != 1007616 || hex.EncodeToString(hash.Sum(nil)) != sessionChinookHash {
		return result, errors.New("sample fixture does not match the admitted source hash")
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return result, errors.New("sample fixture unavailable")
	}
	defer func() { _ = db.Close() }()
	grid, err := sessionDashboardRecordset(ctx, db, sessionInvoiceLinesSQL, invoiceID)
	if err != nil {
		return result, err
	}
	chart, err := sessionDashboardRecordset(ctx, db, sessionInvoiceGenreSQL, invoiceID)
	if err != nil {
		return result, err
	}
	return sessionDashboardResult{Grid: grid, Chart: chart, FixtureSHA256: sessionChinookHash, OutcomeOrigin: "client-reported"}, nil
}

func sessionDashboardRecordset(ctx context.Context, db *sql.DB, query string, invoiceID int64) (apicontract.Recordset, error) {
	out := apicontract.Recordset{Columns: []apicontract.Column{}, Rows: [][]apicontract.TypedValue{}}
	rows, err := db.QueryContext(ctx, query, invoiceID)
	if err != nil {
		return out, errors.New("sample query unavailable")
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil {
		return out, errors.New("sample query unavailable")
	}
	for _, name := range names {
		kind := "integer"
		switch name {
		case "Name", "Genre":
			kind = "string"
		case "UnitPrice", "Total":
			kind = "decimal"
		}
		out.Columns = append(out.Columns, apicontract.Column{Name: name, Type: kind})
	}
	for rows.Next() {
		if len(out.Rows) >= 500 {
			return out, errors.New("sample result exceeds supported dashboard limit")
		}
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		if rows.Scan(dest...) != nil {
			return out, errors.New("sample result unavailable")
		}
		typed := make([]apicontract.TypedValue, len(values))
		for i, value := range values {
			switch v := value.(type) {
			case nil:
				typed[i] = apicontract.NewNullValue()
			case int64:
				typed[i] = apicontract.NewIntegerValue(strconv.FormatInt(v, 10))
			case float64:
				typed[i] = apicontract.NewDecimalValue(strconv.FormatFloat(v, 'f', -1, 64))
			case string:
				typed[i] = apicontract.NewStringValue(v)
			default:
				return out, errors.New("sample result type unsupported")
			}
		}
		out.Rows = append(out.Rows, typed)
	}
	if rows.Err() != nil {
		return out, errors.New("sample query unavailable")
	}
	return out, nil
}

func (c sessionCLI) appliedInvoice(ctx context.Context, snapshot sessioncontext.Snapshot, scope sessioncontext.Scope, admitted sessioncontext.Subject) (apicontract.TypedValue, error) {
	var selected *apicontract.TypedValue
	if snapshot.ContextSet.Enabled {
		for _, slot := range snapshot.ContextSet.Slots {
			if slot.Scope != scope || slot.BagID == "" {
				continue
			}
			for _, ref := range slot.Entries {
				var revision sessioncontext.EntryRevision
				found := false
				for _, current := range snapshot.Revisions {
					if current.EntryID == ref.EntryID && current.ID == ref.RevisionID {
						revision = current
						found = true
						break
					}
				}
				if !found {
					if _, err := c.request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(snapshot.Session.ID)+"/entries/"+url.PathEscape(ref.EntryID)+"/revisions/"+url.PathEscape(ref.RevisionID), nil, &revision); err != nil {
						return apicontract.TypedValue{}, err
					}
				}
				if reflect.DeepEqual(revision.Subject, admitted) && revision.Value.Kind == "scalar" && revision.Value.Scalar != nil && revision.Value.Scalar.Type == apicontract.ValueTypeInteger {
					if selected != nil {
						return apicontract.TypedValue{}, errors.New("dashboard has ambiguous invoice inputs")
					}
					value := *revision.Value.Scalar
					selected = &value
				}
			}
		}
	}
	if selected == nil {
		return apicontract.TypedValue{}, errors.New("apply one admitted scalar Invoice ID before Run")
	}
	return *selected, nil
}

func sessionInvoiceDefinitionDocument() json.RawMessage {
	parameters := []any{map[string]any{"id": "InvoiceId", "type": "integer", "isRequired": true}}
	queries := []any{map[string]any{"id": "invoice-lines", "queryType": "SQL", "text": sessionInvoiceLinesSQL, "connectionId": "chinook-sqlite", "parameters": parameters}, map[string]any{"id": "invoice-by-genre", "queryType": "SQL", "text": sessionInvoiceGenreSQL, "connectionId": "chinook-sqlite", "parameters": parameters}}
	document, _ := json.Marshal(map[string]any{"schemaVersion": 1, "kind": "dashboard", "queries": queries, "widgets": []any{map[string]string{"id": "invoice-lines", "queryId": "invoice-lines"}, map[string]string{"id": "invoice-by-genre", "queryId": "invoice-by-genre"}}})
	return document
}
