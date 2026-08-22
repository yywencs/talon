package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// escapeText 拼出 JSON 文本中的 Unicode 转义序列（反斜杠 + u + 4 位十六进制），
// 避免在源码里书写字面转义。
func escapeText(hex string) string {
	return string(unicodeEscapeProbe) + hex
}

func TestSanitizeJSONForJSONBSkipsPayloadWithoutUnicodeEscapes(t *testing.T) {
	payload := []byte(`{"summary":"中文内容不含转义","total_tokens":12345,"ok":true}`)
	sanitized, err := sanitizeJSONForJSONB(payload)
	require.NoError(t, err)
	assert.Equal(t, string(payload), string(sanitized))
}

func TestSanitizeJSONForJSONBReplacesUnsupportedEscapes(t *testing.T) {
	replacement := string(rune(0xFFFD))
	cases := []struct {
		name      string
		payload   string
		forbidden string
		expected  string
	}{
		{
			name:      "NUL escape",
			payload:   `{"text":"a` + escapeText("0000") + `b"}`,
			forbidden: escapeText("0000"),
			expected:  "a" + replacement + "b",
		},
		{
			name:      "lone high surrogate",
			payload:   `{"text":"a` + escapeText("d83d") + `b"}`,
			forbidden: escapeText("d83d"),
			expected:  "a" + replacement + "b",
		},
		{
			name:      "lone low surrogate",
			payload:   `{"text":"a` + escapeText("dc00") + `b"}`,
			forbidden: escapeText("dc00"),
			expected:  "a" + replacement + "b",
		},
		{
			name:      "high surrogate followed by plain text",
			payload:   `{"text":"` + escapeText("d83d") + `x"}`,
			forbidden: escapeText("d83d"),
			expected:  replacement + "x",
		},
		{
			name:      "uppercase hex lone high surrogate",
			payload:   `{"text":"a` + escapeText("D83D") + `b"}`,
			forbidden: escapeText("D83D"),
			expected:  "a" + replacement + "b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sanitized, err := sanitizeJSONForJSONB([]byte(tc.payload))
			require.NoError(t, err)
			assert.NotContains(t, string(sanitized), tc.forbidden)
			var decoded struct {
				Text string `json:"text"`
			}
			require.NoError(t, json.Unmarshal(sanitized, &decoded))
			assert.Equal(t, tc.expected, decoded.Text)
			// 修复后的 JSON 仍是合法 JSON
			assert.NoError(t, json.Unmarshal(sanitized, &map[string]any{}))
		})
	}
}

func TestSanitizeJSONForJSONBKeepsValidSurrogatePair(t *testing.T) {
	pair := escapeText("d83d") + escapeText("de00")
	sanitized, err := sanitizeJSONForJSONB([]byte(`{"emoji":"a` + pair + `b"}`))
	require.NoError(t, err)
	var decoded struct {
		Emoji string `json:"emoji"`
	}
	require.NoError(t, json.Unmarshal(sanitized, &decoded))
	assert.Equal(t, "a\U0001F600b", decoded.Emoji)
}

func TestSanitizeJSONForJSONBPreservesNumbersAndStructure(t *testing.T) {
	payload := []byte(`{"created_unix_ns":1787392229123456789,"big":12345678901234567890,` +
		`"nested":[{"ok":true,"missing":null,"ratio":1.5,"control":"x` + escapeText("0001") + `y"}],"中文键":"值"}`)
	sanitized, err := sanitizeJSONForJSONB(payload)
	require.NoError(t, err)
	// 大整数不经过 float64，字面值原样保留
	assert.Contains(t, string(sanitized), "1787392229123456789")
	assert.Contains(t, string(sanitized), "12345678901234567890")
	// PG 可接受的转义（控制字符 U+0001）保持转义形式
	assert.Contains(t, string(sanitized), escapeText("0001"))

	var decoded struct {
		CreatedUnixNS json.Number      `json:"created_unix_ns"`
		Nested        []map[string]any `json:"nested"`
		CJKKey        string           `json:"中文键"`
	}
	require.NoError(t, json.Unmarshal(sanitized, &decoded))
	assert.Equal(t, "1787392229123456789", decoded.CreatedUnixNS.String())
	require.Len(t, decoded.Nested, 1)
	assert.Equal(t, true, decoded.Nested[0]["ok"])
	assert.Equal(t, nil, decoded.Nested[0]["missing"])
	assert.Equal(t, 1.5, decoded.Nested[0]["ratio"])
	assert.Equal(t, "x"+string(rune(1))+"y", decoded.Nested[0]["control"])
	assert.Equal(t, "值", decoded.CJKKey)
}

func TestSanitizeJSONForJSONBFixesRawMessagePassthrough(t *testing.T) {
	// 复现生产路径：模型输出自带的非法转义经 json.RawMessage 原样进入
	// json.Marshal 的结果，Go 认为合法、PostgreSQL JSONB 拒绝。
	raw := json.RawMessage(`{"root_cause":"配置回归` + escapeText("d83d") + `，证据` + escapeText("0000") + `缺失"}`)
	payload, err := json.Marshal(map[string]json.RawMessage{"intent": raw})
	require.NoError(t, err)
	assert.Contains(t, string(payload), escapeText("d83d"))

	sanitized, err := sanitizeJSONForJSONB(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(sanitized), escapeText("d83d"))
	assert.NotContains(t, string(sanitized), escapeText("0000"))

	var decoded struct {
		Intent struct {
			RootCause string `json:"root_cause"`
		} `json:"intent"`
	}
	require.NoError(t, json.Unmarshal(sanitized, &decoded))
	replacement := string(rune(0xFFFD))
	assert.Equal(t, "配置回归"+replacement+"，证据"+replacement+"缺失", decoded.Intent.RootCause)
}

func TestSanitizeJSONForJSONBKeepsNonUnicodeEscapesVerbatim(t *testing.T) {
	// 引号、反斜杠与简写控制转义不属于 PostgreSQL 拒绝范围，必须原样保留。
	payload := []byte(`{"quote":"a\"b","back":"c\\d","tab":"e\tf","slash":"g\/h"}`)
	sanitized, err := sanitizeJSONForJSONB(payload)
	require.NoError(t, err)
	assert.Equal(t, string(payload), string(sanitized))
}
