package webui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// LocalAPIVersion is the version of the agent's local HTTP API contract that
// this CLI serves (major.minor). Bump the minor for additive changes, the
// major when a route is removed or changes meaning: every frontend that
// declares "<major" is then refused cleanly instead of half-working.
const LocalAPIVersion = "1.0"

// MinFrontendVersion is the oldest frontend this CLI will serve. It is a
// security/bug floor, bumped only when an older frontend must never run
// against this CLI again; ordinary compatibility is the frontend's own
// requires.localApi range.
const MinFrontendVersion = "0.1.0"

// ErrIncompatible marks a frontend that is installed but cannot be served by this CLI.
var ErrIncompatible = errors.New("web UI is not compatible with this DataTug CLI")

// Compatible checks both sides of the compatibility contract.
func Compatible(m Manifest) error {
	return compatibleWith(m, LocalAPIVersion, MinFrontendVersion)
}

func compatibleWith(m Manifest, localAPI, minFrontend string) error {
	api, err := parseAPIVersion(localAPI)
	if err != nil {
		return err
	}
	rng, err := parseRange(m.Requires.LocalAPI)
	if err != nil {
		return fmt.Errorf("%w: web UI %s has an unreadable requires.localApi: %v", ErrIncompatible, m.Version, err)
	}
	if !rng.satisfiedBy(api) {
		return fmt.Errorf("%w: web UI %s needs local API %s, this CLI serves %s", ErrIncompatible, m.Version, m.Requires.LocalAPI, localAPI)
	}
	if CompareVersions(m.Version, minFrontend) < 0 {
		return fmt.Errorf("%w: web UI %s is older than %s, the oldest this CLI serves", ErrIncompatible, m.Version, minFrontend)
	}
	return nil
}

// apiVersion is a local API contract version, major.minor.
type apiVersion struct{ major, minor int }

func (a apiVersion) cmp(b apiVersion) int {
	switch {
	case a.major != b.major:
		return sign(a.major - b.major)
	default:
		return sign(a.minor - b.minor)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// parseAPIVersion reads "1" or "1.2".
func parseAPIVersion(s string) (apiVersion, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return apiVersion{}, fmt.Errorf("API version %q is not major or major.minor", s)
	}
	var v apiVersion
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return apiVersion{}, fmt.Errorf("API version %q is not major or major.minor", s)
		}
		if i == 0 {
			v.major = n
		} else {
			v.minor = n
		}
	}
	return v, nil
}

type comparator struct {
	op string
	v  apiVersion
}

type apiRange []comparator

// parseRange reads space-separated comparators, all of which must hold: ">=1.0 <2".
func parseRange(s string) (apiRange, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, errors.New("empty range")
	}
	r := make(apiRange, 0, len(fields))
	for _, f := range fields {
		op := ""
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(f, candidate) {
				op = candidate
				break
			}
		}
		if op == "" {
			return nil, fmt.Errorf("comparator %q has no operator (>=, <=, >, <, =)", f)
		}
		v, err := parseAPIVersion(f[len(op):])
		if err != nil {
			return nil, err
		}
		r = append(r, comparator{op: op, v: v})
	}
	return r, nil
}

func (r apiRange) satisfiedBy(v apiVersion) bool {
	for _, c := range r {
		d := v.cmp(c.v)
		ok := false
		switch c.op {
		case ">=":
			ok = d >= 0
		case "<=":
			ok = d <= 0
		case ">":
			ok = d > 0
		case "<":
			ok = d < 0
		default: // "="
			ok = d == 0
		}
		if !ok {
			return false
		}
	}
	return true
}
