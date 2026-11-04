package commands

import (
	"context"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaClientOptions(ctx context.Context, cfg athenaNativeConfig, timeout time.Duration) (out athenasdk.Options, err error) {
	if ctx == nil {
		return out, errAthenaConfig
	}
	defer func() {
		if ctxErr := athenaFresh(ctx); ctxErr != nil {
			out = athenasdk.Options{}
			err = ctxErr
			return
		}
		if err != nil {
			out = athenasdk.Options{}
		}
	}()
	if timeout < time.Millisecond || timeout > 120*time.Second {
		return out, errAthenaConfig
	}
	if err = athenaFresh(ctx); err != nil {
		return out, err
	}
	if len(cfg.Tables) > athenaMaxTables {
		return out, errAthenaConfig
	}
	charge := 0
	for alias, table := range cfg.Tables {
		if err = athenaFresh(ctx); err != nil {
			return out, err
		}
		if !athenaName(alias) || !athenaName(table.Name) ||
			(table.IDColumn != "" && !athenaName(table.IDColumn)) ||
			len(table.Columns) > athenaMaxColumns {
			return out, errAthenaConfig
		}
		charge += len(alias) + len(table.Name) + len(table.IDColumn)
		if charge > athenaMaxConfigCharge {
			return out, errAthenaConfig
		}
		for _, column := range table.Columns {
			if err = athenaFresh(ctx); err != nil {
				return out, err
			}
			if !athenaName(column.Name) || !athenaName(column.Type) {
				return out, errAthenaConfig
			}
			charge += len(column.Name) + len(column.Type) + 128
			if charge > athenaMaxConfigCharge {
				return out, errAthenaConfig
			}
		}
	}
	var identity athenasdk.IdentityProvider
	if identity, err = athenaConfiguredIdentity(cfg.AccountID, cfg.Identity); err != nil {
		return out, err
	}
	_ = cfg.CAFile
	out.Partition, out.Region, out.AccountID = cfg.Partition, cfg.Region, cfg.AccountID
	out.SourceID, out.PolicyRevision, out.WorkGroup = cfg.SourceID, cfg.PolicyRevision, cfg.WorkGroup
	out.Catalog, out.Database, out.OutputLocation = cfg.Catalog, cfg.Database, cfg.OutputLocation
	out.ExpectedBucketOwner, out.MaxScanBytes = cfg.ExpectedBucketOwner, cfg.MaxScanBytes
	out.Encryption = athenasdk.Encryption{Option: cfg.Encryption.Option, KMSKey: cfg.Encryption.KMSKey}
	out.Identity = identity
	out.Timeout = timeout
	out.CursorTTL = timeout
	out.PollInterval = 100 * time.Millisecond
	if timeout < out.PollInterval {
		out.PollInterval = timeout
	}
	out.MaxConcurrent, out.MaxKeys, out.MaxRows = 1, 100, 1000
	out.MaxRequestBytes, out.MaxResponseBytes, out.MaxControlBytes = 65536, 4<<20, 1<<20
	out.MaxPageBytes, out.MaxPages, out.MaxRequests = 1<<20, 100, 256
	out.MaxPolls, out.MaxColumns, out.MaxNodes = 120, athenaMaxColumns, 65536
	out.MaxPageNodes, out.MaxCursorBytes, out.MaxCursorNodes = 8192, 16<<20, 65536
	out.Tables = make(map[string]athenasdk.Table, len(cfg.Tables))
	for alias, table := range cfg.Tables {
		if err = athenaFresh(ctx); err != nil {
			return out, err
		}
		mapped := athenasdk.Table{Name: table.Name, IDColumn: table.IDColumn}
		mapped.Columns = make([]athenasdk.Column, 0, len(table.Columns))
		for _, column := range table.Columns {
			if err = athenaFresh(ctx); err != nil {
				return out, err
			}
			mapped.Columns = append(mapped.Columns, athenasdk.Column{Name: column.Name, Type: column.Type})
		}
		out.Tables[alias] = mapped
	}
	return out, nil
}
