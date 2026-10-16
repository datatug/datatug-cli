package crossverify

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
)

// A default is compared by what it means. The SQLite default expression is evaluated by SQLite itself
// (SELECT <expression>) and the value is put in the canonical form of the column's family (canon.go); the
// server's COLUMN_DEFAULT is parsed into the same form. A default that is no constant (CURRENT_TIMESTAMP,
// CURRENT_DATE, CURRENT_TIME) must be an expression of the server that reads the clock in UTC, in the
// column's own form.

var reCurrent = regexp.MustCompile(`(?i)^(CURRENT_TIMESTAMP|CURRENT_DATE|CURRENT_TIME)$`)

// currentWant is what the printed default of a column with a CURRENT_* default must contain.
func currentWant(family, kw string) string {
	switch strings.ToUpper(kw) {
	case "CURRENT_TIMESTAMP":
		if family == "text" {
			return "date_format(utc_timestamp(),"
		}
		return "utc_timestamp("
	case "CURRENT_DATE":
		if family == "text" {
			return "date_format(utc_timestamp(),"
		}
		return "utc_date("
	}
	if family == "text" {
		return "date_format(utc_timestamp(),"
	}
	return "utc_time("
}

func unwrap(e string) string {
	e = strings.TrimSpace(e)
	for len(e) >= 2 && e[0] == '(' && e[len(e)-1] == ')' {
		depth, closes := 0, -1
		for i := 0; i < len(e) && closes < 0; i++ {
			switch e[i] {
			case '\'':
				for i++; i < len(e) && e[i] != '\''; i++ {
				}
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					closes = i
				}
			}
		}
		if closes != len(e)-1 {
			break
		}
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	return e
}

// sourceDefault is the canonical form of the value SQLite gives for a default expression.
func sourceDefault(ctx context.Context, in Input, ch faithful.Choice, expr string) (string, error) {
	var v any
	if err := in.Source.QueryRowContext(ctx, "SELECT "+expr).Scan(&v); err != nil {
		return "", fmt.Errorf("SQLite cannot evaluate the default %s: %w", expr, err)
	}
	switch x := v.(type) {
	case string:
		// A number written as text in the default of a numeric column.
		switch ch.Family {
		case "int", "bool":
			n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
			if err != nil {
				return "", err
			}
			v = n
		case "float":
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return "", err
			}
			v = f
		}
	case float64:
		if (ch.Family == "int" || ch.Family == "bool") && x == math.Trunc(x) {
			v = int64(x)
		}
	}
	return canonSource(ch, v)
}

// unescapeMySQL undoes the backslash escapes of a MySQL string.
func unescapeMySQL(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '0':
			b.WriteByte(0)
		case 'Z':
			b.WriteByte(26)
		case 'b':
			b.WriteByte(8)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unquoteMySQL reads the string the server prints for an expression default, _utf8mb4\'text\': the text is
// escaped once as part of the printed expression (the quotes that delimit it are escaped too) and once as a
// string of that expression.
func unquoteMySQL(s string) (string, error) {
	s = strings.TrimPrefix(s, "_utf8mb4")
	var body string
	switch {
	case len(s) >= 4 && strings.HasPrefix(s, `\'`) && strings.HasSuffix(s, `\'`):
		body = s[2 : len(s)-2]
	case len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'':
		body = s[1 : len(s)-1]
	default:
		return "", fmt.Errorf("%q is not a string literal", s)
	}
	return unescapeMySQL(unescapeMySQL(body)), nil
}

// targetDefault is the canonical form of COLUMN_DEFAULT.
func targetDefault(ch faithful.Choice, c MyColumn) (string, error) {
	def := c.Default
	switch ch.Family {
	case "int", "bool":
		n, err := strconv.ParseInt(strings.TrimSpace(def), 10, 64)
		if err != nil {
			return "", err
		}
		return "i" + strconv.FormatInt(n, 10), nil
	case "float":
		f, err := strconv.ParseFloat(strings.TrimSpace(def), 64)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("f%016x", math.Float64bits(f)), nil
	case "decimal":
		r, err := ratOf(def)
		if err != nil {
			return "", err
		}
		return "d" + r.String(), nil
	case "date":
		return "D" + def, nil
	case "datetime", "time":
		return "T" + trimFraction(def), nil
	case "text":
		if c.Generated {
			var err error
			if def, err = unquoteMySQL(def); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("t%d:%s", len(def), def), nil
	}
	return "", fmt.Errorf("a default of the %s column family is not compared", ch.Family)
}

// compareDefault checks one column: when the plan carries a default, MySQL must hold the same one.
func compareDefault(ctx context.Context, in Input, c *faithful.ScriptColumn, mc MyColumn, add func(obj, aspect, want, got string)) {
	obj := c.Object
	if c.Default == "" || c.Src.Default == "" || !mc.HasDefault {
		return // the existence of a default is compared by its own aspect
	}
	expr := unwrap(c.Src.Default)
	if reCurrent.MatchString(expr) {
		want := currentWant(c.Choice.Family, expr)
		add(obj, "default expression", "contains "+want, containsOrShow(strings.ToLower(mc.Default), want))
		return
	}
	want, err := sourceDefault(ctx, in, c.Choice, c.Src.Default)
	if err != nil {
		add(obj, "default", "evaluable", err.Error())
		return
	}
	got, err := targetDefault(c.Choice, mc)
	if err != nil {
		add(obj, "default", "readable", err.Error())
		return
	}
	add(obj, "default", want, got)
}

func containsOrShow(s, want string) string {
	if strings.Contains(s, want) {
		return "contains " + want
	}
	return s
}
