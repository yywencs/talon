package storage

import (
	"bytes"
)

// unicodeEscapeProbe 是 JSON 文本中 Unicode 转义的前缀字节（反斜杠 + u）。
// Go 编码器只对控制字符、U+2028/U+2029 使用该转义，普通中文等多字节字符均按
// 原始 UTF-8 输出；因此绝大多数 Artifact 不含该序列，可以直接跳过完整扫描。
var unicodeEscapeProbe = []byte(`\u`)

// replacementEscape 是 U+FFFD 的 Unicode 转义形式，与被替换的非法转义等长。
var replacementEscape = []byte("\\ufffd")

// sanitizeJSONForJSONB 把 JSON 文本修复为 PostgreSQL JSONB 可接受的等价表示。
//
// PostgreSQL 解析 JSONB 时会拒绝字符串中的 NUL 码位（U+0000 的转义）与孤立
// UTF-16 代理对转义（SQLSTATE 22P05 "unsupported Unicode escape sequence"），
// 而 Go 的 encoding/json 认为它们合法：Marshal 会把字符串中的 NUL 字节编码回
// U+0000 转义，并原样保留 json.RawMessage 里外部来源（如模型输出）自带的非法
// 转义。Agent Artifact 与审批参数内嵌大量原始模型 JSON，一旦含有这类转义，
// 整次运行的审计轨迹都无法入库。入库前在持久化边界把无效码位替换为 U+FFFD：
// 这等同于任何 JSON 解码器（包括评测器）本就会执行的替换，只是提前发生，
// 避免运行因不可入库而丢失。
//
// 实现为字节级定点替换：只重写非法的转义序列本身，其余字节（包括数字字面量、
// 键序与空白）原样保留，单次替换前后等长。合法的成对代理转义保持不动，
// PostgreSQL 接受成对代理并会将其组合为增补平面字符。
func sanitizeJSONForJSONB(payload []byte) ([]byte, error) {
	if !bytes.Contains(payload, unicodeEscapeProbe) {
		return payload, nil
	}
	out := make([]byte, 0, len(payload))
	for i := 0; i < len(payload); {
		if payload[i] != '\\' {
			out = append(out, payload[i])
			i++
			continue
		}
		escape, size, replacement := unicodeEscapeAt(payload, i)
		if escape == nil {
			// 非 Unicode 转义（如引号、反斜杠、\b、\t）原样保留；
			// 结尾残缺的反斜杠也按原样拷贝。
			out = append(out, payload[i])
			i++
			continue
		}
		if replacement != nil {
			out = append(out, replacement...)
		} else {
			out = append(out, escape[:size]...)
		}
		i += size
	}
	return out, nil
}

// unicodeEscapeAt 检查 payload[at:] 是否以完整的 Unicode 转义开头。返回值为
// （转义起始切片、应消费的字节数、需要替换为的内容）：
//   - escape 为 nil：不是 Unicode 转义；
//   - replacement 非 nil：该转义非法，用 replacement 替换；
//   - replacement 为 nil：合法转义（含成对代理的整体），按原样拷贝 size 字节。
func unicodeEscapeAt(payload []byte, at int) (escape []byte, size int, replacement []byte) {
	rest := payload[at:]
	if len(rest) < 6 || rest[1] != 'u' {
		return nil, 0, nil
	}
	value, ok := hex4(rest[2:6])
	if !ok {
		return nil, 0, nil
	}
	if value == 0 {
		return rest, 6, replacementEscape
	}
	if isHighSurrogate(value) {
		if len(rest) >= 12 && rest[6] == '\\' && rest[7] == 'u' {
			if low, ok := hex4(rest[8:12]); ok && isLowSurrogate(low) {
				return rest, 12, nil // 成对代理：整体保留
			}
		}
		return rest, 6, replacementEscape
	}
	if isLowSurrogate(value) {
		return rest, 6, replacementEscape
	}
	return rest, 6, nil
}

func hex4(digits []byte) (int, bool) {
	value := 0
	for _, c := range digits {
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		value = value<<4 | d
	}
	return value, true
}

func isHighSurrogate(value int) bool {
	return value >= 0xD800 && value <= 0xDBFF
}

func isLowSurrogate(value int) bool {
	return value >= 0xDC00 && value <= 0xDFFF
}
