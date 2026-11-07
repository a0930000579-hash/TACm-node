package rollup

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// getString 以字符串形式取 map 字段（兼容 json.Number/數值）。
func getString(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	if v := m[key]; v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// getInt64 取整數字段。
func getInt64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// toFloat 取浮點字段。
func toFloat(m map[string]any, key string) (float64, error) {
	switch v := m[key].(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	case string:
		return strconv.ParseFloat(v, 64)
	}
	return 0, fmt.Errorf("not a number: %v", m[key])
}
