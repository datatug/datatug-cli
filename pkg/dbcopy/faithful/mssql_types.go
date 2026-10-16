package faithful

import (
	"fmt"
	"math"
	"strings"
)

// mssqlDialect writes SQL Server 2016+ scripts (T-SQL, loaded with sqlcmd). The types, names, literals and
// rules below were checked against a real SQL Server 2022 container (see scripts/check-journey-mssql.sh).
//
// The script is one transaction: every statement is its own batch ("GO" lines the generator writes), each batch
// starts with a guard that skips it once the transaction is gone, and the marker row is written only after
// every table was counted.
type mssqlDialect struct{}

func init() { registerDialect(mssqlDialect{}) }

func (mssqlDialect) Name() string { return "mssql" }

// MaxIdentLen is the longest name (sysname is nvarchar(128)), counted in UTF-16 code units.
func (mssqlDialect) MaxIdentLen() int { return 128 }

// The key size limits of an index: 900 bytes for a clustered one (the primary key) and 1,700 for a
// nonclustered one (SQL Server 2016 and later).
const (
	mssqlMaxClusteredKey = 900
	mssqlMaxIndexKey     = 1700
	mssqlMaxKeyColumns   = 32
	// mssqlMaxNVarchar is the longest NVARCHAR(n) (in code units) and mssqlMaxVarbinary the longest VARBINARY(n).
	mssqlMaxNVarchar  = 4000
	mssqlMaxVarbinary = 8000
	// mssqlDefaultKeyLen is the length of a text key column that declares none, when the key stays within the limits.
	mssqlDefaultKeyLen = 255
	// mssqlMaxFsp is the most decimals of DATETIME2 and TIME.
	mssqlMaxFsp = 7
)

func mssqlIntType(bits int) string {
	switch bits {
	case 16:
		return "SMALLINT"
	case 32:
		return "INT"
	}
	return "BIGINT"
}

// mssqlRender writes ch.Type from its parts, in the spelling the verifier reads back from the catalog.
func mssqlRender(ch *Choice) {
	switch ch.Family {
	case "int":
		ch.Type = mssqlIntType(ch.Bits)
	case "bool":
		ch.Type = "BIT"
	case "float":
		ch.Type = "FLOAT(53)"
	case "decimal":
		ch.Type = fmt.Sprintf("DECIMAL(%d,%d)", ch.Prec, ch.Scale)
	case "date":
		ch.Type = "DATE"
	case "datetime":
		ch.Type = fmt.Sprintf("DATETIME2(%d)", ch.Fsp)
	case "time":
		ch.Type = fmt.Sprintf("TIME(%d)", ch.Fsp)
	case "text":
		ch.Type = "NVARCHAR(MAX)"
		if ch.Len > 0 && ch.Len <= mssqlMaxNVarchar {
			ch.Type = fmt.Sprintf("NVARCHAR(%d)", ch.Len)
		}
	case "bytes":
		ch.Type = "VARBINARY(MAX)"
		if ch.Len > 0 && ch.Len <= mssqlMaxVarbinary {
			ch.Type = fmt.Sprintf("VARBINARY(%d)", ch.Len)
		}
	}
}

func mssqlChoice(c Choice) Choice { mssqlRender(&c); return c }

func mssqlInt(bits int) Choice { return mssqlChoice(Choice{Kind: KindInt, Family: "int", Bits: bits}) }

func mssqlFloat() Choice { return mssqlChoice(Choice{Kind: KindFloat, Family: "float"}) }

func mssqlDecimal(p, s int) Choice {
	return mssqlChoice(Choice{Kind: KindDecimal, Family: "decimal", Prec: p, Scale: s})
}

// mssqlText is a text column: declLen is the declared length in characters (0 for none), keyed says it must be indexable.
func mssqlText(st *ScriptStats, declLen int, keyed bool) Choice {
	c := Choice{Kind: KindText, Family: "text", MaxSize: st.MaxBytes, Tight: max(st.MaxU16, 1)}
	switch {
	case keyed:
		c.Len = max(declLen, st.MaxU16, 1)
		c.Bounded = declLen == 0
	case declLen > 0 && declLen <= mssqlMaxNVarchar:
		c.Len = declLen
	}
	return mssqlChoice(c)
}

// mssqlBytes is a blob column, or a text column carried as bytes.
func mssqlBytes(st *ScriptStats, keyed bool) Choice {
	size := max(st.MaxBlob, st.MaxBytes)
	c := Choice{Kind: KindBytes, Family: "bytes", MaxSize: size, Tight: max(size, 1)}
	if keyed {
		c.Len = max(size, 1)
		c.Bounded = true
	}
	return mssqlChoice(c)
}

func mssqlDate(st *ScriptStats, family string, kind Kind) Choice {
	c := Choice{Kind: kind, Family: family, Fsp: min(st.MaxFsp, mssqlMaxFsp)}
	if family == "date" {
		c.Fsp = 0
	}
	return mssqlChoice(c)
}

// mssqlComputedDecimal is the DECIMAL that holds every value of a column that declares no precision, or whose
// declared precision is too small; ok is false when the engine's limit of 38 digits is exceeded.
func mssqlComputedDecimal(st *ScriptStats) (Choice, bool) {
	s := st.MaxFracDigits
	p := max(st.MaxIntDigits+s, 1)
	if p > 38 {
		return Choice{}, false
	}
	return mssqlDecimal(p, s), true
}

func (d mssqlDialect) Choose(c *Column, st *ScriptStats, use ColumnUse) Choice {
	ds := st.decl
	declared := strings.TrimSpace(c.SourceType)
	keyed := use.Keyed()
	widen := func(ch Choice, why string) Choice {
		ch.Note = fmt.Sprintf("declared %q but %s: carried as %s", declared, why, ch.Type)
		return ch
	}
	texty := st.only("text") && st.textWithNUL == 0 && st.BadUTF8 == 0
	switch ds.class {
	case declInt:
		if st.only("int") {
			bits := map[int]int{2: 16, 4: 32, 8: 64}[ds.width]
			if ds.width == 2 && (st.minInt < math.MinInt16 || st.maxInt > math.MaxInt16) ||
				ds.width == 4 && (st.minInt < math.MinInt32 || st.maxInt > math.MaxInt32) {
				return widen(mssqlInt(64), "its values do not fit "+mssqlIntType(bits))
			}
			return mssqlInt(bits)
		}
	case declFloat:
		if st.only("int", "real") && st.intInexact == 0 && st.NonFinite == 0 && st.Denormal == 0 {
			return mssqlFloat()
		}
	case declDecimal, declBareNumeric:
		if st.only("int", "real", "text") && st.decOK == st.ints+st.reals+st.texts && st.NonFinite == 0 {
			if ds.class == declDecimal && ds.p <= 38 && ds.s <= ds.p && st.decFit == st.decOK && defaultFitsDecimal(c.Default, ds) {
				ch := mssqlDecimal(ds.p, ds.s)
				ch.Normalized, ch.Example = st.spellings()
				return ch
			}
			if ch, ok := mssqlComputedDecimal(st); ok {
				ch.Normalized, ch.Example = bareSpellings(st, ch.Scale)
				if ds.class == declDecimal {
					return widen(ch, "some values do not fit that precision and scale exactly, or its default does not")
				}
				return ch
			}
		}
	case declBool:
		if st.only("int") && st.nonBool == 0 {
			return mssqlChoice(Choice{Kind: KindBool, Family: "bool"})
		}
	case declDate:
		if st.only("text") && st.dateOK == st.texts {
			return mssqlDate(st, "date", KindDate)
		}
	case declTimestamp:
		if st.only("text") && st.tsOK == st.texts {
			return mssqlDate(st, "datetime", KindTimestamp)
		}
	case declTime:
		if st.only("text") && st.timeOK == st.texts {
			return mssqlDate(st, "time", KindTime)
		}
	case declTimestampTZ, declSpecial:
		if texty {
			return widen(mssqlText(st, 0, keyed), "the script has no such type")
		}
	case declChar:
		if texty {
			if st.MaxU16 <= ds.n {
				return mssqlText(st, ds.n, keyed)
			}
			return widen(mssqlText(st, 0, keyed), "some values are longer than that")
		}
	case declText:
		if texty {
			return mssqlText(st, 0, keyed)
		}
	case declBlob:
		if st.only("blob") {
			return mssqlBytes(st, keyed)
		}
	}
	// A declared type that the values do not fit, or no usable declared type: infer from the values.
	ch := d.infer(st, keyed)
	switch ds.class {
	case declNone:
		if !st.allNull() {
			ch.Note = fmt.Sprintf("has no declared type: carried as %s from its values", ch.Type)
		}
	case declUnknown:
		ch.Note = fmt.Sprintf("declared %q, which has no SQL Server equivalent: carried as %s", declared, ch.Type)
	default:
		ch.Note = fmt.Sprintf("declared %q but its values are not all of that type: carried as %s", declared, ch.Type)
	}
	switch {
	case st.textWithNUL > 0:
		ch.Note += "; a text with a NUL byte is carried as bytes, and a copy back returns BLOBs"
	case st.BadUTF8 > 0:
		ch.Note += "; a text that is not valid UTF-8 cannot be stored in an NVARCHAR column and is carried as bytes"
	case st.NonFinite > 0:
		ch.Note += "; SQL Server has no infinity"
	case st.Denormal > 0:
		ch.Note += "; SQL Server flushes a float below 2.2250738585072014E-308 to zero"
	}
	return ch
}

func (mssqlDialect) infer(st *ScriptStats, keyed bool) Choice {
	switch {
	case st.blobs > 0 || st.textWithNUL > 0 || st.BadUTF8 > 0:
		return mssqlBytes(st, keyed)
	case st.only("int") && !st.allNull():
		return mssqlInt(64)
	case st.only("int", "real") && !st.allNull() && st.decOK == st.ints+st.reals && st.NonFinite == 0:
		if ch, ok := mssqlComputedDecimal(st); ok {
			return ch
		}
	}
	return mssqlText(st, 0, keyed)
}
