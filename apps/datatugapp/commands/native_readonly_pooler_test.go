package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests cannot start native processes: every command is a recording fake.
func TestNativePoolerCIBarrier(t *testing.T) {
	good := map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_OS": "Linux", "DATATUG_NATIVE_POOLER_CI": "1", realPgMajorVar: "17"}
	get := func(key string) string { return good[key] }
	require.True(t, poolerCI("linux", get))
	for _, major := range []string{"17", "18"} {
		good[realPgMajorVar] = major
		require.True(t, poolerCI("linux", get))
	}
	for _, key := range []string{"GITHUB_ACTIONS", "RUNNER_OS", "DATATUG_NATIVE_POOLER_CI", realPgMajorVar} {
		prior := good[key]
		for _, value := range []string{"", "false", "true-ish", "16", "19"} {
			good[key] = value
			require.False(t, poolerCI("linux", get), key)
		}
		good[key] = prior
	}
	require.False(t, poolerCI("darwin", get))
	require.False(t, poolerCI("windows", get))
}

func TestNativePoolerEndpointBeforeProvisioning(t *testing.T) {
	const valid = "postgres://postgres:fixture-only@127.0.0.1:54329/postgres?sslmode=disable"
	calls := 0
	creator := func() realPgServer { calls++; return realPgServer{user: "owned"} }
	server, err := createOwnedPoolerServer(valid, creator)
	require.NoError(t, err)
	require.Equal(t, "owned", server.user)
	require.Equal(t, 1, calls)
	_, err = createOwnedPoolerServer(valid, nil)
	require.Error(t, err)
	for _, test := range []struct{ name, raw string }{
		{"missing", ""},
		{"parse-error", "postgres://%"},
		{"remote-host", strings.Replace(valid, "127.0.0.1", "remote.example", 1)},
		{"host-alias", strings.Replace(valid, "127.0.0.1", "localhost", 1)},
		{"wrong-port", strings.Replace(valid, ":54329", ":5432", 1)},
		{"wrong-database", strings.Replace(valid, "/postgres?", "/other?", 1)},
		{"encoded-database", strings.Replace(valid, "/postgres?", "/post%67res?", 1)},
		{"wrong-scheme", strings.Replace(valid, "postgres://", "postgresql://", 1)},
		{"missing-password", strings.Replace(valid, ":fixture-only@", "@", 1)},
		{"empty-password", strings.Replace(valid, ":fixture-only@", ":@", 1)},
		{"fragment", valid + "#other"},
		{"empty-fragment", valid + "#"},
		{"host-override", valid + "&host=remote.example"},
		{"hostaddr-override", valid + "&hostaddr=192.0.2.1"},
		{"port-override", valid + "&port=5432"},
		{"database-override", valid + "&dbname=other"},
		{"service-override", valid + "&service=other"},
		{"encoded-override", valid + "&%68ost=remote.example"},
		{"options-override", valid + "&options=other"},
		{"duplicate-setting", valid + "&sslmode=disable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := calls
			_, refusal := createOwnedPoolerServer(test.raw, creator)
			require.EqualError(t, refusal, "native pooler: canonical owned CI admin endpoint required")
			require.Equal(t, before, calls, "invalid destination must never call the fixture creator")
		})
	}
}

func TestNativePoolerCleanupRequiresExactOwnership(t *testing.T) {
	id := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	for _, scenario := range []string{"success", "partial-create", "absent", "inventory-error", "different-id", "malformed-id", "wrong-label", "wrong-name", "wrong-image", "inspect-error", "invalid-json", "remove-error", "verification-error", "still-present"} {
		t.Run(scenario, func(t *testing.T) {
			owner := poolerOwner{name: "owned-name", image: image, id: id}
			if scenario == "partial-create" {
				owner.id = "" // Docker created it but did not return an ID.
			}
			removed := false
			owner.run = func(_ context.Context, command string, args ...string) ([]byte, error) {
				require.Equal(t, "docker", command)
				if args[0] == "container" {
					filter := args[5]
					if strings.HasPrefix(filter, "id=") {
						require.Equal(t, "id="+id, filter)
						if scenario == "verification-error" {
							return nil, errors.New("private daemon output")
						}
						if scenario == "still-present" {
							return []byte(id), nil
						}
						return nil, nil
					}
					require.Equal(t, "name=^/owned-name$", filter)
					switch scenario {
					case "absent":
						return nil, nil
					case "inventory-error":
						return nil, errors.New("private daemon output")
					case "different-id":
						return []byte(strings.Repeat("c", 64)), nil
					case "malformed-id":
						return []byte("ambiguous id\n" + id), nil
					}
					return []byte(id), nil
				}
				if args[0] == "inspect" {
					require.Equal(t, id, args[3])
					value := poolerInspection{ID: id, Name: "/owned-name", Image: image}
					value.Config.Labels = map[string]string{"dev.datatug.pooler-runtime": "owned-name"}
					switch scenario {
					case "wrong-label":
						value.Config.Labels["dev.datatug.pooler-runtime"] = "another-owner"
					case "wrong-name":
						value.Name = "/another-owner"
					case "wrong-image":
						value.Image = "sha256:" + strings.Repeat("d", 64)
					case "inspect-error":
						return nil, errors.New("private credential diagnostics")
					case "invalid-json":
						return []byte("private process output"), nil
					}
					return json.Marshal(value)
				}
				require.Equal(t, []string{"rm", "--force", id}, args)
				removed = true
				if scenario == "remove-error" {
					return nil, errors.New("private daemon output")
				}
				return nil, nil
			}
			err := owner.cleanup()
			success := scenario == "success" || scenario == "partial-create" || scenario == "absent"
			if success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private")
			}
			mayRemove := scenario == "success" || scenario == "partial-create" || scenario == "remove-error" || scenario == "verification-error" || scenario == "still-present"
			require.Equal(t, mayRemove, removed, "unowned/ambiguous resources must never be removed")
		})
	}
}

func TestNativePoolerFixtureCredentialDiagnostics(t *testing.T) {
	require.Equal(t, "credential statement [redacted]", firstLine("CREATE ROLE owned LOGIN PASSWORD 'private-secret'"))
	require.Equal(t, "credential statement [redacted]", firstLine("ALTER ROLE owned password 'private-secret'\n"))
	require.Equal(t, "SELECT 1", firstLine("  SELECT 1\nSELECT 2"))
}
