package observability

import otelattribute "go.opentelemetry.io/otel/attribute"

// Attribute 表示 Span 上的一条结构化属性。
type Attribute struct {
	Key   string
	Value any
}

// String 创建字符串属性。
func String(key, value string) Attribute {
	return Attribute{Key: key, Value: value}
}

// Int 创建整数属性。
func Int(key string, value int) Attribute {
	return Attribute{Key: key, Value: value}
}

// Int64 创建 int64 属性。
func Int64(key string, value int64) Attribute {
	return Attribute{Key: key, Value: value}
}

// Bool 创建布尔属性。
func Bool(key string, value bool) Attribute {
	return Attribute{Key: key, Value: value}
}

// Float64 创建浮点属性。
func Float64(key string, value float64) Attribute {
	return Attribute{Key: key, Value: value}
}

// StringSlice 创建字符串切片属性。
func StringSlice(key string, value []string) Attribute {
	return Attribute{Key: key, Value: value}
}

func toOTelAttributes(attrs []Attribute) []otelattribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]otelattribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		switch v := attr.Value.(type) {
		case string:
			out = append(out, otelattribute.String(attr.Key, v))
		case int:
			out = append(out, otelattribute.Int(attr.Key, v))
		case int64:
			out = append(out, otelattribute.Int64(attr.Key, v))
		case bool:
			out = append(out, otelattribute.Bool(attr.Key, v))
		case float64:
			out = append(out, otelattribute.Float64(attr.Key, v))
		case []string:
			out = append(out, otelattribute.StringSlice(attr.Key, v))
		default:
			out = append(out, otelattribute.String(attr.Key, stringifyValue(v)))
		}
	}
	return out
}
