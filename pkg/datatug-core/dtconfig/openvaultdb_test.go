package dtconfig

import "testing"

func TestResolveOpenVaultDBTargetsUsesEnvironment(t *testing.T) {
	t.Setenv("TEST_OVDB_TOKEN", "secret")
	settings := Settings{OpenVaultDB: &OpenVaultDBConfig{Targets: map[string]OpenVaultDBTarget{
		"crm": {BaseURL: "https://vault.example", DatabaseID: "crm", TokenEnv: "TEST_OVDB_TOKEN"},
	}}}
	targets, err := settings.ResolveOpenVaultDBTargets()
	if err != nil {
		t.Fatal(err)
	}
	if got := targets["crm"]; got.Token != "secret" || got.BaseURL != "https://vault.example" || got.DatabaseID != "crm" {
		t.Fatalf("unexpected target: %+v", got)
	}
}
