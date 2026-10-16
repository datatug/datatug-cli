package faithful

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// mysqlDialect writes MySQL 8.x scripts. The types, names, literals and rules below were checked against a
// real MySQL 8.4 server (see scripts/check-journey-mysql.sh).
type mysqlDialect struct{}

func init() { registerDialect(mysqlDialect{}) }

func (mysqlDialect) Name() string { return "mysql" }

func (mysqlDialect) MaxIdentLen() int { return 64 }

// mysqlMaxKeyBytes is InnoDB's limit of one index key.
const mysqlMaxKeyBytes = 3072

// mysqlMaxRowBytes is what a row may weigh: the limit is 65,535 and some bytes go to the row header.
const mysqlMaxRowBytes = 65000

func mysqlIntType(bits int) string {
	switch bits {
	case 16:
		return "SMALLINT"
	case 32:
		return "INT"
	}
	return "BIGINT"
}

func fspSuffix(fsp int) string {
	if fsp <= 0 {
		return ""
	}
	return fmt.Sprintf("(%d)", fsp)
}

// render writes ch.Type from its parts.
func (ch *Choice) render() {
	switch ch.Family {
	case "int":
		ch.Type = mysqlIntType(ch.Bits)
	case "bool":
		ch.Type = "TINYINT(1)"
	case "float":
		ch.Type = "DOUBLE"
	case "decimal":
		ch.Type = fmt.Sprintf("DECIMAL(%d,%d)", ch.Prec, ch.Scale)
	case "date":
		ch.Type = "DATE"
	case "datetime":
		ch.Type = "DATETIME" + fspSuffix(ch.Fsp)
	case "time":
		ch.Type = "TIME" + fspSuffix(ch.Fsp)
	case "text":
		ch.Type = textType(ch.Len, ch.MaxSize)
	case "bytes":
		ch.Type = bytesType(ch.Len, ch.MaxSize)
	}
}

func textType(length, maxBytes int) string {
	switch {
	case length > 0:
		return fmt.Sprintf("VARCHAR(%d)", length)
	case maxBytes <= 65535:
		return "TEXT"
	case maxBytes <= 16777215:
		return "MEDIUMTEXT"
	}
	return "LONGTEXT"
}

func bytesType(length, maxBytes int) string {
	switch {
	case length > 0:
		return fmt.Sprintf("VARBINARY(%d)", length)
	case maxBytes <= 65535:
		return "BLOB"
	case maxBytes <= 16777215:
		return "MEDIUMBLOB"
	}
	return "LONGBLOB"
}

func intChoice(bits int) Choice {
	c := Choice{Kind: KindInt, Family: "int", Bits: bits}
	c.render()
	return c
}

func floatChoice() Choice { c := Choice{Kind: KindFloat, Family: "float"}; c.render(); return c }

func decimalChoice(p, s int) Choice {
	c := Choice{Kind: KindDecimal, Family: "decimal", Prec: p, Scale: s}
	c.render()
	return c
}

// textChoice is a text column: declLen is the declared character length (0 for none), keyed says it must be indexable.
func textChoice(st *ScriptStats, declLen int, keyed bool) Choice {
	c := Choice{Kind: KindText, Family: "text", MaxSize: st.MaxBytes, Tight: max(st.maxLen, 1)}
	switch {
	case keyed:
		c.Len = max(declLen, st.maxLen, 1)
		c.Bounded = declLen == 0
	case declLen > 0 && declLen <= 16383:
		c.Len = declLen
	}
	c.render()
	return c
}

// bytesChoice is a blob column, or a text column carried as bytes.
func bytesChoice(st *ScriptStats, keyed bool) Choice {
	size := max(st.MaxBlob, st.MaxBytes)
	c := Choice{Kind: KindBytes, Family: "bytes", MaxSize: size, Tight: max(size, 1)}
	if keyed {
		c.Len = max(size, 1)
		c.Bounded = true
	}
	c.render()
	return c
}

func (d mysqlDialect) dateChoice(st *ScriptStats, family string, kind Kind) Choice {
	c := Choice{Kind: kind, Family: family, Fsp: min(st.MaxFsp, 6)}
	if family == "date" {
		c.Fsp = 0
	}
	c.render()
	return c
}

// computedDecimal is the DECIMAL that holds every value of a column that declares no precision, or whose
// declared precision is too small; ok is false when the engine's limits (65 digits, 30 decimals) are exceeded.
func computedDecimal(st *ScriptStats) (Choice, bool) {
	s := st.MaxFracDigits
	p := max(st.MaxIntDigits+s, 1)
	if p > 65 || s > 30 {
		return Choice{}, false
	}
	return decimalChoice(p, s), true
}

// bareSpellings counts the decimal texts that DECIMAL(p,s) with the scale of the column writes differently,
// with the first example (the one with the fewest decimals).
func bareSpellings(st *ScriptStats, scale int) (int64, string) {
	n, ex := st.textNonCanon, st.nonCanonExample
	fracs := make([]int, 0, len(st.canonFrac))
	for f := range st.canonFrac {
		if f < scale {
			fracs = append(fracs, f)
		}
	}
	sort.Ints(fracs)
	for _, f := range fracs {
		n += st.canonFrac[f]
		if ex == "" {
			ex = st.canonFracExample[f]
		}
	}
	return n, ex
}

func (d mysqlDialect) Choose(c *Column, st *ScriptStats, use ColumnUse) Choice {
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
				return widen(intChoice(64), "its values do not fit "+mysqlIntType(bits))
			}
			return intChoice(bits)
		}
	case declFloat:
		if st.only("int", "real") && st.intInexact == 0 && st.NonFinite == 0 {
			return floatChoice()
		}
	case declDecimal, declBareNumeric:
		if st.only("int", "real", "text") && st.decOK == st.ints+st.reals+st.texts && st.NonFinite == 0 {
			if ds.class == declDecimal && ds.p <= 65 && ds.s <= 30 && ds.s <= ds.p && st.decFit == st.decOK && defaultFitsDecimal(c.Default, ds) {
				ch := decimalChoice(ds.p, ds.s)
				ch.Normalized, ch.Example = st.spellings()
				return ch
			}
			if ch, ok := computedDecimal(st); ok {
				ch.Normalized, ch.Example = bareSpellings(st, ch.Scale)
				if ds.class == declDecimal {
					return widen(ch, "some values do not fit that precision and scale exactly, or its default does not")
				}
				return ch
			}
		}
	case declBool:
		if st.only("int") && st.nonBool == 0 {
			ch := Choice{Kind: KindBool, Family: "bool"}
			ch.render()
			return ch
		}
	case declDate:
		if st.only("text") && st.dateOK == st.texts && st.LowYear == 0 {
			return d.dateChoice(st, "date", KindDate)
		}
	case declTimestamp:
		if st.only("text") && st.tsOK == st.texts && st.LowYear == 0 {
			return d.dateChoice(st, "datetime", KindTimestamp)
		}
	case declTime:
		if st.only("text") && st.timeOK == st.texts {
			return d.dateChoice(st, "time", KindTime)
		}
	case declTimestampTZ, declSpecial:
		if texty {
			return widen(textChoice(st, 0, keyed), "MySQL has no such type")
		}
	case declChar:
		if texty {
			if st.maxLen <= ds.n {
				return textChoice(st, ds.n, keyed)
			}
			return widen(textChoice(st, 0, keyed), "some values are longer than that")
		}
	case declText:
		if texty {
			return textChoice(st, 0, keyed)
		}
	case declBlob:
		if st.only("blob") {
			return bytesChoice(st, keyed)
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
		ch.Note = fmt.Sprintf("declared %q, which has no MySQL equivalent: carried as %s", declared, ch.Type)
	default:
		ch.Note = fmt.Sprintf("declared %q but its values are not all of that type: carried as %s", declared, ch.Type)
	}
	switch {
	case st.textWithNUL > 0:
		ch.Note += "; a text with a NUL byte is carried as bytes, and a copy back returns BLOBs"
	case st.BadUTF8 > 0:
		ch.Note += "; a text that is not valid UTF-8 cannot be stored in a utf8mb4 column and is carried as bytes"
	case st.NonFinite > 0:
		ch.Note += "; MySQL has no infinity"
	}
	return ch
}

func (mysqlDialect) infer(st *ScriptStats, keyed bool) Choice {
	switch {
	case st.blobs > 0 || st.textWithNUL > 0 || st.BadUTF8 > 0:
		return bytesChoice(st, keyed)
	case st.only("int") && !st.allNull():
		return intChoice(64)
	case st.only("int", "real") && !st.allNull() && st.decOK == st.ints+st.reals && st.NonFinite == 0:
		if ch, ok := computedDecimal(st); ok {
			return ch
		}
	}
	return textChoice(st, 0, keyed)
}
