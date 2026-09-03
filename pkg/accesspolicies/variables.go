package accesspolicies

import (
	"fmt"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"gopkg.in/yaml.v3"
)

// ParseVariables turns name=value pairs into policy variables. Names follow
// DALgo's parameter grammar; currentUser, principal.* and path.* are reserved
// for --as, --role, --group and path captures. Values are YAML scalars or flow
// sequences so numbers, booleans and lists keep their type; quote a value to
// force a string; a value that is not valid YAML is kept as the literal string.
func ParseVariables(pairs []string) (map[string]any, error) {
	variables := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		name, raw, ok := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid --var %q: want name=value", pair)
		}
		if !dal.ValidParamName(name) {
			return nil, fmt.Errorf("invalid --var %q: name %q is not a valid parameter name", pair, name)
		}
		if name == "currentUser" || strings.HasPrefix(name, "principal.") || strings.HasPrefix(name, "path.") {
			return nil, fmt.Errorf("invalid --var %q: %q is reserved; use --as, --role and --group", pair, name)
		}
		value := parseValue(raw)
		if _, isMap := value.(map[string]any); isMap {
			return nil, fmt.Errorf("invalid --var %q: a mapping is not a valid variable value", pair)
		}
		variables[name] = value
	}
	return variables, nil
}

func parseValue(raw string) any {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var value any
	if err := yaml.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return raw
	}
	if _, isTime := value.(time.Time); isTime {
		return raw // dates stay the literal text so they compare with stored strings
	}
	return value
}
