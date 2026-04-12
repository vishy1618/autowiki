package auth

import "encoding/json"

func parseJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
