package chat

import (
	"encoding/json"
)

// lookupValue and lookupValueToken are ui.go's original
// query_parameters_dialog.go helpers, kept unchanged: chatui_query_overlay.go's
// parameterLookupOverlay uses them to turn an arbitrary cell value into the
// comparable/JSON-encodable token used to key its selected-rows map.

func lookupValue(value any) any {
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return value
}

func lookupValueToken(value any) (string, bool) {
	switch lookupValue(value).(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
	default:
		return "", false
	}
	encoded, err := json.Marshal(lookupValue(value))
	return string(encoded), err == nil
}
