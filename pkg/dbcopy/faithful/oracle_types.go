package faithful

import (
	"fmt"
	"math"
	"strings"
)

// oracleDialect writes Oracle Database 23ai scripts (SQL*Plus). The types, names, literals and rules below were
// checked against a real Oracle Database Free container (see scripts/check-journey-oracle.sh).
//
// Oracle commits every DDL statement, so the script is not one transaction. It stops at the first error
// (WHENEVER SQLERROR, written in the script), and its last statement writes the completion marker in the same
// PL/SQL block that counts every table, so a load that lost rows is never marked complete, whatever the client does
// after an error.
type oracleDialect struct{}

func init() { registerDialect(oracleDialect{}) }

func (oracleDialect) Name() string { return "oracle" }

// MaxIdentLen is the longest name: 128 bytes (Oracle 12.2 and later). The limit counts bytes, which this
// interface counts as characters, so StorableName checks the bytes itself.
func (oracleDialect) MaxIdentLen() int { return 128 }

// The limits of the engine the script is written for.
const (
	// oracleMaxVarchar is the longest VARCHAR2 in bytes (MAX_STRING_SIZE=STANDARD, the default) and oracleMaxRaw the
	// longest RAW; a longer text is a CLOB and longer bytes are a BLOB.
	oracleMaxVarchar = 4000
	oracleMaxRaw     = 2000
	// oracleMaxKeyBytes is the budget of one index key. Oracle refuses a key row longer than about 6,398 bytes with an
	// 8 KB block (ORA-01450); the script keeps every key under this smaller number.
	oracleMaxKeyBytes   = 6000
	oracleMaxKeyColumns = 32
	// oracleDefaultKeyLen is the length in characters of a text key column that declares none, when the key stays
	// within the budget.
	oracleDefaultKeyLen = 255
	// oracleMaxFsp is the most decimals of a TIMESTAMP.
	oracleMaxFsp = 9
	// oracleByteSemantics marks (in Choice.Prec, which only a decimal otherwise uses) a VARCHAR2 whose length counts
	// bytes: the column of a text that declares no length.
	oracleByteSemantics = 1
)

func oracleIntType(bits int) string {
	switch bits {
	case 16:
		return "NUMBER(5,0)"
	case 32:
		return "NUMBER(10,0)"
	}
	return "NUMBER(19,0)"
}

// oracleRender writes ch.Type from its parts, in the spelling the verifier reads back from the catalog.
func oracleRender(ch *Choice) {
	switch ch.Family {
	case "int":
		ch.Type = oracleIntType(ch.Bits)
	case "bool":
		ch.Type = "NUMBER(1,0)"
	case "float":
		ch.Type = "BINARY_DOUBLE"
	case "decimal":
		ch.Type = fmt.Sprintf("NUMBER(%d,%d)", ch.Prec, ch.Scale)
	case "date":
		ch.Type = "DATE"
	case "datetime":
		ch.Type = fmt.Sprintf("TIMESTAMP(%d)", ch.Fsp)
	case "text":
		ch.Type = "CLOB"
		if ch.Len > 0 {
			unit := "CHAR"
			if ch.Prec == oracleByteSemantics {
				unit = "BYTE"
			}
			ch.Type = fmt.Sprintf("VARCHAR2(%d %s)", ch.Len, unit)
		}
	case "bytes":
		ch.Type = "BLOB"
		if ch.Len > 0 && ch.Len <= oracleMaxRaw {
			ch.Type = fmt.Sprintf("RAW(%d)", ch.Len)
		}
	}
}

func oracleChoice(c Choice) Choice { oracleRender(&c); return c }

func oracleInt(bits int) Choice {
	return oracleChoice(Choice{Kind: KindInt, Family: "int", Bits: bits})
}

func oracleFloat() Choice { return oracleChoice(Choice{Kind: KindFloat, Family: "float"}) }

func oracleDecimal(p, s int) Choice {
	return oracleChoice(Choice{Kind: KindDecimal, Family: "decimal", Prec: p, Scale: s})
}

// oracleText is a text column: declLen is the declared length in characters (0 for none), keyed says it must be
// indexable. Tight is counted in characters.
func oracleText(st *ScriptStats, declLen int, keyed bool) Choice {
	c := Choice{Kind: KindText, Family: "text", MaxSize: st.MaxBytes, Tight: max(st.maxLen, 1)}
	switch {
	case keyed:
		c.Len = min(max(declLen, st.maxLen, 1), oracleMaxVarchar)
		c.Bounded = declLen == 0
	case st.MaxBytes > oracleMaxVarchar:
		// a CLOB: no VARCHAR2 holds it
	case declLen > 0 && declLen <= oracleMaxVarchar:
		c.Len = declLen
	default:
		c.Len, c.Prec = oracleMaxVarchar, oracleByteSemantics
	}
	return oracleChoice(c)
}

// oracleBytes is a blob column, or a text column carried as bytes: a key gets a RAW of its longest value, anything
// else a BLOB (which keeps an empty value apart from NULL).
func oracleBytes(st *ScriptStats, keyed bool) Choice {
	size := max(st.MaxBlob, st.MaxBytes)
	c := Choice{Kind: KindBytes, Family: "bytes", MaxSize: size, Tight: max(size, 1)}
	if keyed {
		c.Len = max(size, 1)
		c.Bounded = true
	}
	return oracleChoice(c)
}

func oracleDate(st *ScriptStats, family string, kind Kind) Choice {
	c := Choice{Kind: kind, Family: family, Fsp: min(st.MaxFsp, oracleMaxFsp)}
	if family == "date" {
		c.Fsp = 0
	}
	return oracleChoice(c)
}

// oracleComputedDecimal is the NUMBER that holds every value of a column that declares no precision, or whose
// declared precision is too small; ok is false when the engine's limit of 38 digits is exceeded.
func oracleComputedDecimal(st *ScriptStats) (Choice, bool) {
	s := st.MaxFracDigits
	p := max(st.MaxIntDigits+s, 1)
	if p > 38 {
		return Choice{}, false
	}
	return oracleDecimal(p, s), true
}

// oracleDecimalSpellings counts the decimal texts that NUMBER spells differently (it keeps no trailing zero), with the
// first example.
func oracleDecimalSpellings(st *ScriptStats) (int64, string) { return st.trimmed, st.trimmedExample }

func (d oracleDialect) Choose(c *Column, st *ScriptStats, use ColumnUse) Choice {
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
				return widen(oracleInt(64), "its values do not fit "+oracleIntType(bits))
			}
			return oracleInt(bits)
		}
	case declFloat:
		if st.only("int", "real") && st.intInexact == 0 {
			return oracleFloat()
		}
	case declDecimal, declBareNumeric:
		if st.only("int", "real", "text") && st.decOK == st.ints+st.reals+st.texts && st.NonFinite == 0 {
			if ds.class == declDecimal && ds.p <= 38 && ds.s <= ds.p && st.decFit == st.decOK && defaultFitsDecimal(c.Default, ds) {
				ch := oracleDecimal(ds.p, ds.s)
				ch.Normalized, ch.Example = oracleDecimalSpellings(st)
				return ch
			}
			if ch, ok := oracleComputedDecimal(st); ok {
				ch.Normalized, ch.Example = oracleDecimalSpellings(st)
				if ds.class == declDecimal {
					return widen(ch, "some values do not fit that precision and scale exactly, or its default does not")
				}
				return ch
			}
		}
	case declBool:
		if st.only("int") && st.nonBool == 0 {
			return oracleChoice(Choice{Kind: KindBool, Family: "bool"})
		}
	case declDate:
		if st.only("text") && st.dateOK == st.texts && st.GregorianGap == 0 {
			return oracleDate(st, "date", KindDate)
		}
	case declTimestamp:
		if st.only("text") && st.tsOK == st.texts && st.GregorianGap == 0 {
			return oracleDate(st, "datetime", KindTimestamp)
		}
	case declTime:
		if texty && st.timeOK == st.texts {
			return widen(oracleText(st, max(st.maxLen, 8), keyed), "Oracle has no time of day type")
		}
	case declTimestampTZ, declSpecial:
		if texty {
			return widen(oracleText(st, 0, keyed), "the script has no such type")
		}
	case declChar:
		if texty {
			if st.maxLen <= ds.n {
				return oracleText(st, ds.n, keyed)
			}
			return widen(oracleText(st, 0, keyed), "some values are longer than that")
		}
	case declText:
		if texty {
			return oracleText(st, 0, keyed)
		}
	case declBlob:
		if st.only("blob") {
			return oracleBytes(st, keyed)
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
		ch.Note = fmt.Sprintf("declared %q, which has no Oracle equivalent: carried as %s", declared, ch.Type)
	default:
		ch.Note = fmt.Sprintf("declared %q but its values are not all of that type: carried as %s", declared, ch.Type)
		if (ds.class == declDate || ds.class == declTimestamp) && st.GregorianGap > 0 {
			ch.Note = fmt.Sprintf("declared %q but %d value(s), the first %q, fall in the days 1582-10-05 to 1582-10-14 that Oracle's calendar skips (it would store them ten days later): carried as %s", declared, st.GregorianGap, st.GapExample, ch.Type)
		}
	}
	switch {
	case st.textWithNUL > 0:
		ch.Note += "; a text with a NUL byte is carried as bytes, and a copy back returns BLOBs"
	case st.BadUTF8 > 0:
		ch.Note += "; a text that is not valid UTF-8 cannot be stored in a VARCHAR2 column and is carried as bytes"
	}
	return ch
}

func (oracleDialect) infer(st *ScriptStats, keyed bool) Choice {
	switch {
	case st.blobs > 0 || st.textWithNUL > 0 || st.BadUTF8 > 0:
		return oracleBytes(st, keyed)
	case st.only("int") && !st.allNull():
		return oracleInt(64)
	case st.only("int", "real") && !st.allNull() && st.decOK == st.ints+st.reals && st.NonFinite == 0:
		if ch, ok := oracleComputedDecimal(st); ok {
			return ch
		}
	}
	return oracleText(st, 0, keyed)
}
