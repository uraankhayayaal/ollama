package tools

import "encoding/json"

// ParseArguments разбирает JSON-строку аргументов в map.
func ParseArguments(raw string) (map[string]any, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}
