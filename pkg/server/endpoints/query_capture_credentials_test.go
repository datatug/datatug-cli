package endpoints

import "testing"

func TestCaptureCredentialReason(t *testing.T) {
	tests := []struct {
		name  string
		value string
		found bool
	}{
		{"URL userinfo with a password", "postgres://user:secret@host/db", true},
		{"URL embedded in a longer value", "jdbc:postgresql://user:secret@host/db", true},
		{"percent-encoded password", "postgres://user:%73ecret@host/db", true},
		{"explicitly empty password", "postgres://user:@host/db", true},
		{"URL inside prose", "copy postgres://u:p@h/db into the tool", true},
		{"unparseable URL authority", "postgres://u:p@h%zz/db", true},
		{"DSN userinfo", "user:secret@tcp(host:3306)/db", true},
		{"key/value password", "Server=h;User Id=u;Password=secret;", true},
		{"pwd key", "uid=u;pwd=secret", true},
		{"password in a URL query", "postgres://u@h/db?password=secret", true},
		{"password literal in DTQL", "where:\n  right:\n    value: \"Password=secret\"\n", true},

		{"username alone in a URL", "postgres://user@host/db", false},
		{"URL with no userinfo", "https://example.com/a?b=c", false},
		{"an e-mail address", "ask ops@example.com", false},
		{"an empty password key", "Server=h;Password=;", false},
		{"a password column name", "password_hash is never selected", false},
		{"plain prose", "Which invoices does this customer have?", false},
		{"parameterized DTQL", "from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    field: CustomerId\n  right:\n    param: Customer.ID\n", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, found := captureCredentialReason(tt.value)
			if found != tt.found {
				t.Fatalf("captureCredentialReason(%q) found = %v (%q), want %v", tt.value, found, reason, tt.found)
			}
			if found && reason == "" {
				t.Errorf("a refusal must say why")
			}
		})
	}
}
