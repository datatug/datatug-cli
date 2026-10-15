package faithful

import (
	"errors"
	"strings"
	"testing"
)

// scenarioChecks say what each scenario's copies must have done. They run on the replay of the
// recording, with no fault injected, so they pin the behaviour that the recording was made with.
var scenarioChecks = map[string]func(t *testing.T, e *scenarioEnv){
	"sqlite_values_to_pg": func(t *testing.T, e *scenarioEnv) {
		c := e.copies
		if len(c) != 5 || len(e.diffs) != 2 {
			t.Fatalf("copies %d", len(c))
		}
		for i := 0; i < 5; i++ {
			if c[i].err != nil {
				t.Fatalf("copy %d: %v", i+1, c[i].err)
			}
		}
		rep := c[0].rep
		for obj, why := range map[string]string{
			"vals.f4w": "not held exactly by a 4-byte real", "vals.jbw": "jsonb would rewrite 2 of its values", "vals.tzw": "offsets other than +00",
			"vals.nul": "returns its values as BLOBs", "vals.wide": "do not fit that precision",
		} {
			if w := findWarning(rep, WarnTypeWidened, obj); w == nil || !strings.Contains(w.Message, why) {
				t.Errorf("%s: %v", obj, w)
			}
		}
		for _, obj := range []string{"times.tm", "times.tz", "times.tz2"} {
			if w := findWarning(rep, WarnTypeWidened, obj); w == nil || !strings.Contains(w.Message, "carried as text") {
				t.Errorf("%s: a leap second, an hour 24 and a day that is not there stay text: %v", obj, w)
			}
		}
		if !hasWarningText(rep, "tnum.t", "numeric default 0.00 of a text column") {
			t.Errorf("a numeric default of a text column is said: %v", rep.Warnings)
		}
		if !hasWarningText(rep, "vals.dtn", "1 text value(s) are written in numeric's spelling (-0.00 -> 0.00)") || findWarning(rep, WarnTypeWidened, "vals.dtn") != nil ||
			!hasWarningText(rep, "vals.dt", "text default '0.00' of a decimal text column became the numeric default 0.00") {
			t.Errorf("decimal texts are numeric and the new spelling is counted: %v", rep.Warnings)
		}
		if w := findWarning(c[4].rep, WarnTypeWidened, "vals.dtn"); w == nil || !strings.Contains(w.Message, "--decimals=text") || hasWarning(c[4].rep, WarnValuesNormalized, "vals.dtn") {
			t.Errorf("--decimals=text keeps the text: %v", c[4].rep.Warnings)
		}
		for _, obj := range []string{"vals.f4", "vals.jb", "vals.tz", "vals.b", "vals.s"} {
			if w := findWarning(rep, WarnTypeWidened, obj); w != nil {
				t.Errorf("%s keeps its type: %v", obj, w)
			}
		}
		// The way back: every value is what it was but the column that held a NUL byte and the decimal text, which say so.
		if len(e.diffs[0]) != 3 || e.diffs[0][0].Object != "tnum.t" || e.diffs[0][1].Object != "vals.dtn" || e.diffs[0][2].Object != "vals.nul" {
			t.Errorf("sqlite -> postgres -> sqlite: %v", e.diffs[0])
		}
		if len(e.diffs[1]) != 0 {
			t.Errorf("postgres -> sqlite -> postgres: %v", e.diffs[1])
		}
		// The reload wrote the same rows again.
		if c[3].err != nil || c[3].rep.Totals.Rows != 9 {
			t.Errorf("reload: %v", c[3].err)
		}
	},
	"pg_values": func(t *testing.T, e *scenarioEnv) {
		c := e.copies
		if len(c) != 2 || c[0].err != nil || c[1].err != nil {
			t.Fatalf("copies: %+v", c)
		}
		rep := c[0].rep
		for obj, want := range map[string][]string{
			"fl.r":        {"1 NaN value(s) became NULL", "1 negative zero value(s) became 0"},
			"fl.d":        {"1 negative zero value(s) became 0"},
			"fl.c":        {"blank-padded"},
			"sq_clash.id": {"counter of the source was at 1 but a row has id 2"},
			"sq_new.id":   {"starts at 100 and steps by 5", "next value of the source is 100, the target counts on from 100"},
		} {
			for _, w := range want {
				if !hasWarningText(rep, obj, w) {
					t.Errorf("%s: no warning with %q in %v", obj, w, rep.Warnings)
				}
			}
		}
		if hasWarning(rep, WarnIdentityNotCarried, "sq_plain.id") || hasWarning(rep, WarnIdentityNotCarried, "sq_used.id") {
			t.Errorf("a sequence that starts at 1 and steps by 1 is carried: %v", rep.Warnings)
		}
		// The counters: the next value of a sequence that was never called is its start; one that was called is past its last value.
		for table, want := range map[string]string{"sq_new": "99", "sq_plain": "", "sq_used": "2"} {
			if got := e.sequenceOf(t, table); got != want {
				t.Errorf("%s counter: %q, want %q", table, got, want)
			}
		}
	},
	"filters_and_reload": func(t *testing.T, e *scenarioEnv) {
		c := e.copies
		if len(c) != 15 {
			t.Fatalf("copies %d", len(c))
		}
		if c[0].err != nil || !hasWarning(c[0].rep, WarnFKNotValidated, "child(parent_id)") {
			t.Errorf("a filtered copy that leaves a parent out: %v", c[0].err)
		}
		if w := findWarning(c[0].rep, WarnFKNotValidated, "child(parent_id)"); w == nil || !strings.Contains(w.Message, "--where / --limit left the parent out") {
			t.Errorf("the reason is the filter: %v", w)
		}
		for i, want := range map[int]string{
			1: `table "nope" does not exist in the source`, 2: `table "child" is not part of this copy`, 3: `unknown field "nope"`,
			4: `value "x" cannot be coerced`, 5: `unsupported operator "<>"`, 6: `value "x" cannot be coerced`,
		} {
			if c[i].err == nil || !strings.Contains(c[i].err.Error(), want) || c[i].rep.ExitCode != 2 {
				t.Errorf("copy %d: %v", i+1, c[i].err)
			}
		}
		if c[7].err != nil {
			t.Errorf("reload: %v", c[7].err)
		}
		for i, want := range map[int]string{
			8: `column "label" is missing`, 9: `column "y" is text and the source values are bigint`, 10: `extra column "z" is NOT NULL without a default`,
			11: `primary key () is not the source's (id)`,
		} {
			var mm *ReloadMismatchError
			if !errors.As(c[i].err, &mm) || !strings.Contains(c[i].err.Error(), want) || c[i].rep.ExitCode != 1 {
				t.Errorf("copy %d: %v", i+1, c[i].err)
			}
		}
		if c[12].err != nil {
			t.Errorf("a target with an extra column that has a default is reloaded: %v", c[12].err)
		}
		if c[13].err != nil || !hasWarning(c[13].rep, WarnFKNotValidated, "child(parent_id)") {
			t.Errorf("a reload that leaves a parent out: %v %v", c[13].err, c[13].rep.Warnings)
		}
		if c[14].err == nil || !strings.Contains(c[14].err.Error(), `"keyless" on the target is not a table`) {
			t.Errorf("a view in the way: %v", c[14].err)
		}
	},
	"filters_from_pg": func(t *testing.T, e *scenarioEnv) {
		c := e.copies
		if len(c) != 3 {
			t.Fatalf("copies %d", len(c))
		}
		// From PostgreSQL: where and limit are read through the server.
		if c[0].err != nil || c[0].rep.Totals.Rows == 0 {
			t.Errorf("filtered from postgres: %v", c[0].err)
		}
		var mm *ReloadMismatchError
		if c[1].err != nil || !errors.As(c[2].err, &mm) || !strings.Contains(c[2].err.Error(), `column "uid" is missing`) {
			t.Errorf("reload into sqlite: %v / %v", c[1].err, c[2].err)
		}
	},
	"sqlite_to_sqlite": func(t *testing.T, e *scenarioEnv) {
		c := e.copies
		if len(c) != 13 {
			t.Fatalf("copies %d", len(c))
		}
		// copies 8..: filtered, reload, mismatch, sidecar, merge
		if c[7].err != nil || !hasWarningText(c[7].rep, "child -> Parent", "--where / --limit left the parent out") {
			t.Errorf("filtered: %v %v", c[7].err, c[7].rep.Warnings)
		}
		if c[8].err != nil {
			t.Errorf("reload: %v", c[8].err)
		}
		var mm *ReloadMismatchError
		if !errors.As(c[9].err, &mm) {
			t.Errorf("mismatch: %v", c[9].err)
		}
		if c[10].err != nil || !hasWarning(c[10].rep, WarnObjectNotCarried, legacySidecar) || c[10].rep.Totals.Tables != 1 {
			t.Errorf("a sidecar table of an earlier copy is not data: %v", c[10].err)
		}
		if c[11].err != nil || c[11].rep.Totals.Tables != 1 || hasWarning(c[11].rep, WarnObjectNotCarried, legacySidecar) {
			t.Errorf("named, it is copied: %v", c[11].err)
		}
		if c[12].err == nil || c[12].rep.ExitCode != 2 {
			t.Errorf("merge: %v", c[12].err)
		}
	},
}

func findWarning(rep *Report, kind, object string) *Warning {
	for i := range rep.Warnings {
		if rep.Warnings[i].Kind == kind && rep.Warnings[i].Object == object {
			return &rep.Warnings[i]
		}
	}
	return nil
}

func hasWarningText(rep *Report, object, text string) bool {
	for _, w := range rep.Warnings {
		if w.Object == object && strings.Contains(w.Message, text) {
			return true
		}
	}
	return false
}
