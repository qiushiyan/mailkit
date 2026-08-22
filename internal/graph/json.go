package graph

import "encoding/json/v2"

func unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func marshal(v any) ([]byte, error) { return json.Marshal(v) }
