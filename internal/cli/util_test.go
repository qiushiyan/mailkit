package cli_test

import "encoding/json/v2"

func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
