package watchdaemon

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Config belongs to the daemon deployment. Projects carry only coordination
// endpoint and namespace; credentials and storage are never read from a project.
type Config struct {
	Namespace      string            `json:"namespace"`
	Listen         string            `json:"listen,omitempty"`
	TLSCertFile    string            `json:"tlsCertFile,omitempty"`
	TLSKeyFile     string            `json:"tlsKeyFile,omitempty"`
	LicenseKey     string            `json:"licenseKey,omitempty"`
	Credentials    []Credential      `json:"credentials"`
	AllowedOrigins []string          `json:"allowedOrigins,omitempty"`
	PublicUIURL    string            `json:"publicUIURL,omitempty"`
	ProjectUIURLs  map[string]string `json:"projectUIURLs,omitempty"`
	Limits         Limits            `json:"limits,omitempty"`
	Storage        struct {
		DALGOConnectionEnv string `json:"dalgoConnectionEnv"`
	} `json:"storage"`
	Runner struct {
		Cloud               *CloudRunnerConfig `json:"cloud,omitempty"`
		TokenEnv            string             `json:"tokenEnv"`
		CoordinatorEndpoint string             `json:"coordinatorEndpoint"`
		ProjectDir          string             `json:"projectDir"`
		Environment         string             `json:"environment"`
		Principal           string             `json:"principal"`
		EvidenceDir         string             `json:"evidenceDir"`
		// PrivateExecutionEvidence routes immutable receipts and snapshots outside
		// the project for deployments that mount trusted project files read-only.
		PrivateExecutionEvidence bool               `json:"privateExecutionEvidence,omitempty"`
		AllowOpaqueSQL           bool               `json:"allowOpaqueSQL,omitempty"`
		Telegram                 *TelegramBotConfig `json:"telegram,omitempty"`
	} `json:"runner"`
	Notifications struct {
		EmailSink string      `json:"emailSink,omitempty"`
		SMTP      *SMTPConfig `json:"smtp,omitempty"`
		SMSSink   string      `json:"smsSink,omitempty"`
		Twilio    bool        `json:"twilio,omitempty"`
	} `json:"notifications"`
}

type Limits struct {
	EmailPerDay        int `json:"emailPerDay,omitempty"`
	EmailPerMinute     int `json:"emailPerMinute,omitempty"`
	SMSPerDay          int `json:"smsPerDay,omitempty"`
	SMSPerMinute       int `json:"smsPerMinute,omitempty"`
	TelegramPerDay     int `json:"telegramPerDay,omitempty"`
	TelegramPerMinute  int `json:"telegramPerMinute,omitempty"`
	WatchesPerUser     int `json:"watchesPerUser,omitempty"`
	MinIntervalSeconds int `json:"minIntervalSeconds,omitempty"`
}

func (l Limits) Resolve() (Limits, error) {
	values := []struct {
		p             *int
		fallback, max int
	}{{&l.EmailPerDay, 50, 5000}, {&l.EmailPerMinute, 5, 500}, {&l.SMSPerDay, 10, 100}, {&l.SMSPerMinute, 2, 10}, {&l.TelegramPerDay, 100, 1000}, {&l.TelegramPerMinute, 5, 100}, {&l.WatchesPerUser, 100, 1000}, {&l.MinIntervalSeconds, 5, 3600}}
	for _, v := range values {
		if *v.p == 0 {
			*v.p = v.fallback
		}
		if *v.p < 1 || *v.p > v.max {
			return Limits{}, errors.New("watch deployment limits are outside supported bounds")
		}
	}
	if l.MinIntervalSeconds < 5 {
		return Limits{}, errors.New("watch minimum interval must be at least five seconds")
	}
	return l, nil
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("watch daemon configuration is unavailable")
	}
	// This read-only descriptor has no buffered state to commit.
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return cfg, errors.New("watch daemon configuration must be a bounded regular JSON file")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1024*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil {
		return cfg, errors.New("watch daemon configuration is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return cfg, errors.New("watch daemon configuration is invalid")
	}
	return cfg, nil
}
