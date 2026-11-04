package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAthenaCommandRegisteredStaticMetadata(t *testing.T) {
	group := queryCommand()
	athenaCmd, _, err := group.Find([]string{"athena"})
	if err != nil || athenaCmd == nil || athenaCmd.Name() != "athena" {
		t.Fatalf("athena not registered: %v", err)
	}
	for _, name := range []string{"config", "request", "timeout"} {
		if athenaCmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
	if timeout, err := athenaCmd.Flags().GetDuration("timeout"); err != nil || timeout != 10*time.Second {
		t.Fatalf("timeout default = %v, %v", timeout, err)
	}
	if athenaCmd.Flags().Lookup("controls") != nil {
		t.Fatal("interactive --controls flag advertised")
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(athenaConfigTestJSON), &config); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile := filepath.Join(dir, "missing-ca.pem")
	config["caFile"] = caFile
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	identity := config["identity"].(map[string]any)
	envNames := []string{identity["accessKeyIdEnv"].(string), identity["secretAccessKeyEnv"].(string)}
	for _, name := range envNames {
		t.Setenv(name, "")
	}

	var out, stderr bytes.Buffer
	group.SetIn(strings.NewReader(`{"operation":"metadata"}`))
	group.SetOut(&out)
	group.SetErr(&stderr)
	group.SetArgs([]string{"athena", "--config", configPath, "--request", "-"})
	if err := group.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), athenaPolicyNote) {
		t.Fatalf("policy note missing: %q", stderr.String())
	}

	frame := out.Bytes()
	if len(frame) == 0 || frame[len(frame)-1] != '\n' || bytes.Count(frame, []byte{'\n'}) != 1 {
		t.Fatalf("output is not one JSONL object: %q", frame)
	}
	var result map[string]any
	if err := json.Unmarshal(frame[:len(frame)-1], &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 5 || result["operation"] != "metadata" || result["format"] != athenaPointFormat ||
		result["provenance"] != athenaMetadataProvenance || result["dynamicDiscovery"] != false {
		t.Fatalf("metadata envelope = %#v", result)
	}

	tables, ok := result["tables"].([]any)
	if !ok || len(tables) != 1 {
		t.Fatalf("tables = %#v", result["tables"])
	}
	table, ok := tables[0].(map[string]any)
	if !ok || len(table) != 4 || table["alias"] != "items" || table["name"] != "actual" || table["idColumn"] != "" {
		t.Fatalf("table mapping = %#v", tables[0])
	}
	wantColumns := []map[string]any{
		{"name": "id", "type": "varchar"},
		{"name": "amount", "type": "decimal(38,10)"},
	}
	gotColumns, ok := table["columns"].([]any)
	if !ok || len(gotColumns) != len(wantColumns) {
		t.Fatalf("columns = %#v", table["columns"])
	}
	for i, want := range wantColumns {
		got, ok := gotColumns[i].(map[string]any)
		if !ok || len(got) != 2 || got["name"] != want["name"] || got["type"] != want["type"] {
			t.Fatalf("column %d = %#v", i, gotColumns[i])
		}
	}

	text := string(frame)
	forbidden := append([]string{caFile}, envNames...)
	forbidden = append(forbidden, "sql", "queryID", "cursor", "receipt")
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("output contains %q: %s", value, text)
		}
	}
}

func TestAthenaCommandOrdinaryStdinAndCallerRestoration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(athenaConfigTestJSON), 0600); err != nil {
		t.Fatal(err)
	}
	const usageMessage = "Athena configuration or request is invalid or unreadable"

	exitDetails := func(t *testing.T, err error) (int, string) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error")
		}
		var coder interface{ ExitCode() int }
		if !errors.As(err, &coder) {
			t.Fatalf("error %v does not carry an exit code", err)
		}
		return coder.ExitCode(), err.Error()
	}

	runRegistered := func(t *testing.T, stdin, config, request string) (string, string, error) {
		t.Helper()
		group := queryCommand()
		athenaCmd, _, err := group.Find([]string{"athena"})
		if err != nil || athenaCmd == nil || athenaCmd.Name() != "athena" {
			t.Fatalf("athena not registered: %v", err)
		}
		var out, stderr bytes.Buffer
		group.SetIn(strings.NewReader(stdin))
		group.SetOut(&out)
		group.SetErr(&stderr)
		group.SetArgs([]string{"athena", "--config", config, "--request", request})
		err = group.Execute()
		return out.String(), stderr.String(), err
	}

	runDirect := func(t *testing.T, parent context.Context, stdin, config, request string, timeout time.Duration) (context.Context, context.Context, *bytes.Buffer, *bytes.Buffer, error) {
		t.Helper()
		cmd := queryAthenaCommand()
		if parent != nil {
			cmd.SetContext(parent)
		}
		initial := cmd.Context()
		var out, stderr bytes.Buffer
		cmd.SetIn(strings.NewReader(stdin))
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		if err := cmd.Flags().Set("config", config); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Flags().Set("request", request); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Flags().Set("timeout", timeout.String()); err != nil {
			t.Fatal(err)
		}
		err := cmd.RunE(cmd, nil)
		return initial, cmd.Context(), &out, &stderr, err
	}

	t.Run("registeredMetadataStdin", func(t *testing.T) {
		out, stderr, err := runRegistered(t, "{\n  \"operation\": \"metadata\"\n}\n \t\n", configPath, "-")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr, athenaPolicyNote) {
			t.Fatalf("policy note missing: %q", stderr)
		}
		frame := []byte(out)
		if len(frame) == 0 || frame[len(frame)-1] != '\n' || bytes.Count(frame, []byte{'\n'}) != 1 {
			t.Fatalf("output is not one JSONL object: %q", frame)
		}
		var result map[string]any
		if err := json.Unmarshal(frame[:len(frame)-1], &result); err != nil {
			t.Fatal(err)
		}
		if result["operation"] != "metadata" || result["format"] != athenaPointFormat {
			t.Fatalf("metadata envelope = %#v", result)
		}

		for _, test := range []struct {
			name  string
			stdin string
		}{
			{"secondObject", "{\"operation\":\"metadata\"}\n{\"operation\":\"metadata\"}"},
			{"duplicateOperation", "{\"operation\":\"metadata\",\"operation\":\"metadata\"}"},
			{"wrongCaseField", "{\"Operation\":\"metadata\"}"},
			{"nullOperation", "{\"operation\":null}"},
			{"unsupportedExtraField", "{\"operation\":\"metadata\",\"sql\":\"select 1\"}"},
		} {
			t.Run("refuses/"+test.name, func(t *testing.T) {
				out, _, err := runRegistered(t, test.stdin, configPath, "-")
				if out != "" {
					t.Fatalf("stdout = %q, want empty", out)
				}
				code, message := exitDetails(t, err)
				if code != exitCodeUsage || message != usageMessage {
					t.Fatalf("exit = %d %q", code, message)
				}
			})
		}
	})

	invalidArgs := []struct {
		name    string
		config  string
		request string
		timeout time.Duration
	}{
		{"missingConfig", "", "-", time.Second},
		{"missingRequest", configPath, "", time.Second},
		{"zeroTimeout", configPath, "-", 0},
	}

	t.Run("invalidArgumentsAreUsage", func(t *testing.T) {
		for _, test := range invalidArgs {
			t.Run(test.name, func(t *testing.T) {
				_, _, out, _, err := runDirect(t, nil, `{"operation":"metadata"}`, test.config, test.request, test.timeout)
				if out.Len() != 0 {
					t.Fatalf("stdout = %q, want empty", out.String())
				}
				code, message := exitDetails(t, err)
				if code != exitCodeUsage || message != usageMessage {
					t.Fatalf("exit = %d %q", code, message)
				}
			})
		}
	})

	t.Run("callerPriority", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancelExpired()
		for _, parent := range []struct {
			name    string
			ctx     context.Context
			message string
		}{
			{"canceled", canceled, "Athena read cancelled"},
			{"deadline", expired, "Athena read deadline exceeded"},
		} {
			for _, test := range invalidArgs {
				t.Run(parent.name+"/"+test.name, func(t *testing.T) {
					initial, final, out, _, err := runDirect(t, parent.ctx, `{"operation":"metadata"}`, test.config, test.request, test.timeout)
					if initial != parent.ctx || final != initial {
						t.Fatal("caller context identity was not restored")
					}
					if out.Len() != 0 {
						t.Fatalf("stdout = %q, want empty", out.String())
					}
					code, message := exitDetails(t, err)
					if code != exitCodeDatabase || message != parent.message {
						t.Fatalf("exit = %d %q", code, message)
					}
				})
			}
		}
	})

	t.Run("nilOriginalContextRestored", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			stdin   string
			config  string
			request string
			wantErr bool
			code    int
			message string
		}{
			{"success", "{\n \"operation\": \"metadata\"\n}\n", configPath, "-", false, 0, ""},
			{"failure", `{"operation":"metadata"}`, "", "-", true, exitCodeUsage, usageMessage},
		} {
			t.Run(test.name, func(t *testing.T) {
				initial, final, out, _, err := runDirect(t, nil, test.stdin, test.config, test.request, time.Second)
				if initial != nil {
					t.Fatalf("initial context = %v, want nil", initial)
				}
				if final != initial {
					t.Fatalf("final context = %v, want original nil", final)
				}
				if test.wantErr {
					if out.Len() != 0 {
						t.Fatalf("stdout = %q, want empty", out.String())
					}
					code, message := exitDetails(t, err)
					if code != test.code || message != test.message {
						t.Fatalf("exit = %d %q", code, message)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if out.Len() == 0 {
					t.Fatal("success produced no stdout")
				}
			})
		}
	})
}
