package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

var errAthenaConfig = errors.New("athena configuration is invalid; expected one bounded strict JSON object")

const (
	athenaMaxTables          = 256
	athenaMaxColumns         = 128
	athenaMaxNameBytes       = 4096
	athenaMaxOutputBytes     = 2048
	athenaMaxSubjectBytes    = 512
	athenaMaxGenerationBytes = 128
	athenaMinScanBytes       = 10_000_000
	athenaMaxConfigCharge    = 64 << 10
)

var (
	athenaRegionPattern    = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$`)
	athenaAccountPattern   = regexp.MustCompile(`^[0-9]{12}$`)
	athenaWorkGroupPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)
	athenaScanPattern      = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type athenaNativeConfig struct {
	Partition           string
	Region              string
	AccountID           string
	SourceID            string
	PolicyRevision      string
	WorkGroup           string
	Catalog             string
	Database            string
	OutputLocation      string
	ExpectedBucketOwner string
	Encryption          athenaEncryptionConfig
	MaxScanBytes        int64
	Tables              map[string]athenaTableConfig
	Identity            athenaIdentityConfig
	CAFile              string
}

type athenaEncryptionConfig struct {
	Option string
	KMSKey string
}

type athenaTableConfig struct {
	Name     string
	IDColumn string
	Columns  []athenaColumnConfig
}

type athenaColumnConfig struct {
	Name string
	Type string
}

type athenaIdentityConfig struct {
	Subject            string
	Generation         string
	AccessKeyIDEnv     string
	SecretAccessKeyEnv string
	SessionTokenEnv    string
}

func athenaFields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &fields); err != nil || fields == nil {
		return nil, errAthenaConfig
	}
	if err := convexControlFields(fields, names...); err != nil {
		return nil, errAthenaConfig
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, errAthenaConfig
		}
	}
	return fields, nil
}

func athenaObjectMap(raw []byte) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := decodeDynamoDocument(raw, &values); err != nil || values == nil {
		return nil, errAthenaConfig
	}
	return values, nil
}

func athenaString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errAthenaConfig
	}
	return value, nil
}

func athenaName(value string) bool {
	return value != "" && len(value) <= athenaMaxNameBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func athenaBoundedText(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func athenaEnv(value string) bool {
	return len(value) <= athenaMaxNameBytes && dbcopy.ValidEnvName(value)
}

func athenaPartition(value string) bool {
	return value == "aws" || value == "aws-cn" || value == "aws-us-gov"
}

func athenaPartitionRegion(partition, region string) bool {
	switch partition {
	case "aws":
		return !strings.HasPrefix(region, "cn-") && !strings.HasPrefix(region, "us-gov-")
	case "aws-cn":
		return strings.HasPrefix(region, "cn-")
	case "aws-us-gov":
		return strings.HasPrefix(region, "us-gov-")
	}
	return false
}

func athenaOutputLocation(value string) bool {
	if len(value) > athenaMaxOutputBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "s3" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Port() != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Hostname() == "" || parsed.Host != parsed.Hostname() {
		return false
	}
	return parsed.Path != "" && strings.HasSuffix(parsed.Path, "/")
}

func athenaScanBytes(raw json.RawMessage) (int64, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || !athenaScanPattern.MatchString(text) {
		return 0, errAthenaConfig
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < athenaMinScanBytes {
		return 0, errAthenaConfig
	}
	return value, nil
}

func athenaFresh(ctx context.Context) error {
	err := dynamoFresh(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

func athenaTable(ctx context.Context, raw []byte) (athenaTableConfig, error) {
	var table athenaTableConfig
	fields, err := athenaFields(raw, "name", "idColumn", "columns")
	if err != nil {
		return table, err
	}
	nameRaw, hasName := fields["name"]
	columnsRaw, hasColumns := fields["columns"]
	if !hasName || !hasColumns {
		return table, errAthenaConfig
	}
	if table.Name, err = athenaString(nameRaw); err != nil {
		return table, err
	}
	if !athenaName(table.Name) {
		return table, errAthenaConfig
	}
	if value, ok := fields["idColumn"]; ok {
		if table.IDColumn, err = athenaString(value); err != nil {
			return table, err
		}
		if table.IDColumn != "" && !athenaName(table.IDColumn) {
			return table, errAthenaConfig
		}
	}
	var rawColumns []json.RawMessage
	if err := json.Unmarshal(columnsRaw, &rawColumns); err != nil || len(rawColumns) == 0 || len(rawColumns) > athenaMaxColumns {
		return table, errAthenaConfig
	}
	table.Columns = make([]athenaColumnConfig, 0, len(rawColumns))
	seen := make(map[string]bool, len(rawColumns))
	for _, rawColumn := range rawColumns {
		if err := athenaFresh(ctx); err != nil {
			return athenaTableConfig{}, err
		}
		columnFields, err := athenaFields(rawColumn, "name", "type")
		if err != nil {
			return athenaTableConfig{}, err
		}
		nameRaw, hasName := columnFields["name"]
		typeRaw, hasType := columnFields["type"]
		if !hasName || !hasType {
			return athenaTableConfig{}, errAthenaConfig
		}
		var column athenaColumnConfig
		if column.Name, err = athenaString(nameRaw); err != nil {
			return athenaTableConfig{}, err
		}
		if column.Type, err = athenaString(typeRaw); err != nil {
			return athenaTableConfig{}, err
		}
		if !athenaName(column.Name) || !athenaName(column.Type) || seen[column.Name] {
			return athenaTableConfig{}, errAthenaConfig
		}
		seen[column.Name] = true
		table.Columns = append(table.Columns, column)
	}
	if table.IDColumn != "" {
		found := false
		for _, column := range table.Columns {
			if column.Name == table.IDColumn {
				if column.Type != "varchar" && column.Type != "string" && column.Type != "char" {
					return athenaTableConfig{}, errAthenaConfig
				}
				found = true
				break
			}
		}
		if !found {
			return athenaTableConfig{}, errAthenaConfig
		}
	}
	return table, nil
}

func decodeAthenaConfig(ctx context.Context, raw []byte) (out athenaNativeConfig, err error) {
	if ctx == nil {
		return out, errAthenaConfig
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			out = athenaNativeConfig{}
			err = ctxErr
			return
		}
		if err != nil {
			out = athenaNativeConfig{}
		}
	}()
	if err = athenaFresh(ctx); err != nil {
		return out, err
	}
	fields, err := athenaFields(raw,
		"partition", "region", "accountId", "sourceId", "policyRevision", "workGroup", "catalog",
		"database", "outputLocation", "expectedBucketOwner", "encryption", "maxScanBytes", "tables", "identity", "caFile")
	if err != nil {
		return out, err
	}
	for _, name := range []string{"region", "accountId", "sourceId", "policyRevision", "workGroup", "catalog", "database", "outputLocation", "expectedBucketOwner", "encryption", "maxScanBytes", "tables", "identity"} {
		if _, ok := fields[name]; !ok {
			return out, errAthenaConfig
		}
	}
	partition := "aws"
	if value, ok := fields["partition"]; ok {
		if partition, err = athenaString(value); err != nil {
			return out, err
		}
	}
	if !athenaPartition(partition) {
		return out, errAthenaConfig
	}
	out.Partition = partition
	if out.Region, err = athenaString(fields["region"]); err != nil {
		return out, err
	}
	if !athenaRegionPattern.MatchString(out.Region) || !athenaPartitionRegion(partition, out.Region) {
		return out, errAthenaConfig
	}
	if out.AccountID, err = athenaString(fields["accountId"]); err != nil {
		return out, err
	}
	if !athenaAccountPattern.MatchString(out.AccountID) {
		return out, errAthenaConfig
	}
	if out.SourceID, err = athenaString(fields["sourceId"]); err != nil {
		return out, err
	}
	if !athenaName(out.SourceID) {
		return out, errAthenaConfig
	}
	if out.PolicyRevision, err = athenaString(fields["policyRevision"]); err != nil {
		return out, err
	}
	if !athenaName(out.PolicyRevision) {
		return out, errAthenaConfig
	}
	if out.WorkGroup, err = athenaString(fields["workGroup"]); err != nil {
		return out, err
	}
	if !athenaWorkGroupPattern.MatchString(out.WorkGroup) {
		return out, errAthenaConfig
	}
	if out.Catalog, err = athenaString(fields["catalog"]); err != nil {
		return out, err
	}
	if !athenaName(out.Catalog) {
		return out, errAthenaConfig
	}
	if out.Database, err = athenaString(fields["database"]); err != nil {
		return out, err
	}
	if !athenaName(out.Database) {
		return out, errAthenaConfig
	}
	if out.OutputLocation, err = athenaString(fields["outputLocation"]); err != nil {
		return out, err
	}
	if !athenaOutputLocation(out.OutputLocation) {
		return out, errAthenaConfig
	}
	if out.ExpectedBucketOwner, err = athenaString(fields["expectedBucketOwner"]); err != nil {
		return out, err
	}
	if !athenaAccountPattern.MatchString(out.ExpectedBucketOwner) {
		return out, errAthenaConfig
	}
	encryption, err := athenaFields(fields["encryption"], "option", "kmsKey")
	if err != nil {
		return out, err
	}
	optionRaw, hasOption := encryption["option"]
	if !hasOption {
		return out, errAthenaConfig
	}
	if out.Encryption.Option, err = athenaString(optionRaw); err != nil {
		return out, err
	}
	switch out.Encryption.Option {
	case "SSE_S3":
		if _, ok := encryption["kmsKey"]; ok {
			return out, errAthenaConfig
		}
	case "SSE_KMS":
		kmsRaw, ok := encryption["kmsKey"]
		if !ok {
			return out, errAthenaConfig
		}
		if out.Encryption.KMSKey, err = athenaString(kmsRaw); err != nil {
			return out, err
		}
		if !athenaBoundedText(out.Encryption.KMSKey, athenaMaxNameBytes) {
			return out, errAthenaConfig
		}
	default:
		return out, errAthenaConfig
	}
	if out.MaxScanBytes, err = athenaScanBytes(fields["maxScanBytes"]); err != nil {
		return out, err
	}
	tableValues, err := athenaObjectMap(fields["tables"])
	if err != nil || len(tableValues) == 0 || len(tableValues) > athenaMaxTables {
		return out, errAthenaConfig
	}
	out.Tables = make(map[string]athenaTableConfig, len(tableValues))
	charge := 0
	for alias, rawTable := range tableValues {
		if err := athenaFresh(ctx); err != nil {
			return out, err
		}
		if !athenaName(alias) {
			return out, errAthenaConfig
		}
		table, err := athenaTable(ctx, rawTable)
		if err != nil {
			return out, err
		}
		charge += len(alias) + len(table.Name) + len(table.IDColumn)
		for _, column := range table.Columns {
			charge += len(column.Name) + len(column.Type) + 128
		}
		if charge > athenaMaxConfigCharge {
			return out, errAthenaConfig
		}
		out.Tables[alias] = table
	}
	identity, err := athenaFields(fields["identity"], "subject", "generation", "accessKeyIdEnv", "secretAccessKeyEnv", "sessionTokenEnv")
	if err != nil {
		return out, err
	}
	for _, name := range []string{"subject", "generation", "accessKeyIdEnv", "secretAccessKeyEnv"} {
		if _, ok := identity[name]; !ok {
			return out, errAthenaConfig
		}
	}
	if out.Identity.Subject, err = athenaString(identity["subject"]); err != nil {
		return out, err
	}
	if !athenaBoundedText(out.Identity.Subject, athenaMaxSubjectBytes) {
		return out, errAthenaConfig
	}
	if out.Identity.Generation, err = athenaString(identity["generation"]); err != nil {
		return out, err
	}
	if !athenaBoundedText(out.Identity.Generation, athenaMaxGenerationBytes) {
		return out, errAthenaConfig
	}
	if out.Identity.AccessKeyIDEnv, err = athenaString(identity["accessKeyIdEnv"]); err != nil {
		return out, err
	}
	if !athenaEnv(out.Identity.AccessKeyIDEnv) {
		return out, errAthenaConfig
	}
	if out.Identity.SecretAccessKeyEnv, err = athenaString(identity["secretAccessKeyEnv"]); err != nil {
		return out, err
	}
	if !athenaEnv(out.Identity.SecretAccessKeyEnv) {
		return out, errAthenaConfig
	}
	if value, ok := identity["sessionTokenEnv"]; ok {
		if out.Identity.SessionTokenEnv, err = athenaString(value); err != nil {
			return out, err
		}
		if !athenaEnv(out.Identity.SessionTokenEnv) {
			return out, errAthenaConfig
		}
	}
	if value, ok := fields["caFile"]; ok {
		if out.CAFile, err = athenaString(value); err != nil {
			return out, err
		}
		if !athenaBoundedText(out.CAFile, athenaMaxNameBytes) {
			return out, errAthenaConfig
		}
	}
	if err = athenaFresh(ctx); err != nil {
		return out, err
	}
	return out, nil
}
