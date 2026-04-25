package file_editor

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const encodingProbeLimitBytes int64 = 1 * 1024 * 1024

type textEncodingKind string

const (
	textEncodingUTF8    textEncodingKind = "utf8"
	textEncodingUTF16LE textEncodingKind = "utf16le"
	textEncodingUTF16BE textEncodingKind = "utf16be"
	textEncodingGB18030 textEncodingKind = "gb18030"
)

type detectedTextEncoding struct {
	Kind     textEncodingKind
	Name     string
	HasBOM   bool
	Editable bool
	Reason   string
}

func defaultUTF8Encoding() detectedTextEncoding {
	return detectedTextEncoding{
		Kind:     textEncodingUTF8,
		Name:     "UTF-8",
		Editable: true,
	}
}

func readTextFile(path string) (string, detectedTextEncoding, error) {
	sample, fullyReadBySample, err := readFileSample(path, encodingProbeLimitBytes)
	if err != nil {
		return "", detectedTextEncoding{}, fmt.Errorf("读取文件探测样本失败: %w", err)
	}

	encoding, err := detectTextEncoding(sample)
	if err != nil {
		return "", detectedTextEncoding{}, err
	}

	data := sample
	if !fullyReadBySample {
		data, err = os.ReadFile(path)
		if err != nil {
			return "", detectedTextEncoding{}, fmt.Errorf("读取文件内容失败: %w", err)
		}
	}

	text, err := decodeText(data, encoding)
	if err != nil {
		return "", detectedTextEncoding{}, err
	}
	return text, encoding, nil
}

func writeTextFile(path, content string, fileEncoding detectedTextEncoding, perm os.FileMode) error {
	data, err := encodeText(content, fileEncoding)
	if err != nil {
		return err
	}
	return writeFileAtomically(path, data, perm)
}

func detectTextEncoding(sample []byte) (detectedTextEncoding, error) {
	if len(sample) == 0 {
		return defaultUTF8Encoding(), nil
	}

	if bytes.HasPrefix(sample, []byte{0xEF, 0xBB, 0xBF}) {
		return detectedTextEncoding{
			Kind:     textEncodingUTF8,
			Name:     "UTF-8",
			HasBOM:   true,
			Editable: true,
			Reason:   "检测到 UTF-8 BOM",
		}, nil
	}
	if bytes.HasPrefix(sample, []byte{0xFF, 0xFE}) {
		return detectedTextEncoding{
			Kind:     textEncodingUTF16LE,
			Name:     "UTF-16 LE",
			HasBOM:   true,
			Editable: true,
			Reason:   "检测到 UTF-16 LE BOM",
		}, nil
	}
	if bytes.HasPrefix(sample, []byte{0xFE, 0xFF}) {
		return detectedTextEncoding{
			Kind:     textEncodingUTF16BE,
			Name:     "UTF-16 BE",
			HasBOM:   true,
			Editable: true,
			Reason:   "检测到 UTF-16 BE BOM",
		}, nil
	}
	if encoding, ok := detectUTF16WithoutBOM(sample); ok {
		return encoding, nil
	}
	if isBinaryContent(sample) {
		return detectedTextEncoding{}, fmt.Errorf("检测到二进制文件，不支持文本查看或编辑")
	}
	if utf8.Valid(sample) {
		return detectedTextEncoding{
			Kind:     textEncodingUTF8,
			Name:     "UTF-8",
			Editable: true,
			Reason:   "样本满足 UTF-8",
		}, nil
	}
	if encoding, ok := detectGB18030(sample); ok {
		return encoding, nil
	}

	return detectedTextEncoding{}, fmt.Errorf("无法确认文件编码；当前仅支持 UTF-8、UTF-8 BOM、UTF-16 LE/BE、GB18030")
}

func detectUTF16WithoutBOM(sample []byte) (detectedTextEncoding, bool) {
	if len(sample) < 4 {
		return detectedTextEncoding{}, false
	}

	pairCount := 0
	zeroPairs := 0
	lePrintablePairs := 0
	bePrintablePairs := 0

	for i := 0; i+1 < len(sample); i += 2 {
		lo := sample[i]
		hi := sample[i+1]
		pairCount++
		if lo == 0 || hi == 0 {
			zeroPairs++
		}
		if hi == 0 && isLikelyTextByte(lo) {
			lePrintablePairs++
		}
		if lo == 0 && isLikelyTextByte(hi) {
			bePrintablePairs++
		}
	}

	if pairCount == 0 || zeroPairs == 0 {
		return detectedTextEncoding{}, false
	}
	if float64(zeroPairs)/float64(pairCount) < 0.2 {
		return detectedTextEncoding{}, false
	}

	leScore := float64(lePrintablePairs) / float64(pairCount)
	beScore := float64(bePrintablePairs) / float64(pairCount)
	if leScore >= 0.6 && leScore > beScore+0.15 {
		return detectedTextEncoding{
			Kind:     textEncodingUTF16LE,
			Name:     "UTF-16 LE",
			Editable: false,
			Reason:   "样本疑似 UTF-16 LE，但未检测到 BOM",
		}, true
	}
	if beScore >= 0.6 && beScore > leScore+0.15 {
		return detectedTextEncoding{
			Kind:     textEncodingUTF16BE,
			Name:     "UTF-16 BE",
			Editable: false,
			Reason:   "样本疑似 UTF-16 BE，但未检测到 BOM",
		}, true
	}
	return detectedTextEncoding{}, false
}

func detectGB18030(sample []byte) (detectedTextEncoding, bool) {
	if !containsNonASCIIByte(sample) {
		return detectedTextEncoding{}, false
	}

	decoded, err := decodeGB18030Bytes(sample)
	if err != nil {
		return detectedTextEncoding{}, false
	}
	roundTrip, err := encodeGB18030String(string(decoded))
	if err != nil || !bytes.Equal(roundTrip, sample) {
		return detectedTextEncoding{}, false
	}

	score, hasHan := scoreDecodedText(string(decoded))
	if score < 0.85 || !hasHan {
		return detectedTextEncoding{}, false
	}

	return detectedTextEncoding{
		Kind:     textEncodingGB18030,
		Name:     "GB18030",
		Editable: true,
		Reason:   "样本符合 GB18030 文本特征",
	}, true
}

func decodeText(data []byte, fileEncoding detectedTextEncoding) (string, error) {
	switch fileEncoding.Kind {
	case textEncodingUTF8:
		if fileEncoding.HasBOM && bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
			data = data[3:]
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("文件编码识别为 UTF-8，但内容不是合法 UTF-8")
		}
		return string(data), nil
	case textEncodingUTF16LE:
		return decodeUTF16(data, binary.LittleEndian, fileEncoding.HasBOM)
	case textEncodingUTF16BE:
		return decodeUTF16(data, binary.BigEndian, fileEncoding.HasBOM)
	case textEncodingGB18030:
		decoded, err := decodeGB18030Bytes(data)
		if err != nil {
			return "", fmt.Errorf("按 GB18030 解码失败: %w", err)
		}
		return string(decoded), nil
	default:
		return "", fmt.Errorf("不支持的文件编码: %s", fileEncoding.Name)
	}
}

func encodeText(content string, fileEncoding detectedTextEncoding) ([]byte, error) {
	switch fileEncoding.Kind {
	case textEncodingUTF8:
		data := []byte(content)
		if fileEncoding.HasBOM {
			return append([]byte{0xEF, 0xBB, 0xBF}, data...), nil
		}
		return data, nil
	case textEncodingUTF16LE:
		return encodeUTF16(content, binary.LittleEndian, fileEncoding.HasBOM), nil
	case textEncodingUTF16BE:
		return encodeUTF16(content, binary.BigEndian, fileEncoding.HasBOM), nil
	case textEncodingGB18030:
		data, err := encodeGB18030String(content)
		if err != nil {
			return nil, fmt.Errorf("按 GB18030 编码失败: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("不支持的文件编码: %s", fileEncoding.Name)
	}
}

func decodeUTF16(data []byte, order binary.ByteOrder, hasBOM bool) (string, error) {
	if hasBOM {
		if len(data) < 2 {
			return "", fmt.Errorf("UTF-16 BOM 不完整")
		}
		data = data[2:]
	}
	if len(data)%2 != 0 {
		return "", fmt.Errorf("UTF-16 字节长度不是 2 的倍数")
	}

	words := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		words = append(words, order.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(words)), nil
}

func encodeUTF16(content string, order binary.ByteOrder, hasBOM bool) []byte {
	runes := utf16.Encode([]rune(content))
	data := make([]byte, 0, len(runes)*2+2)
	if hasBOM {
		if order == binary.LittleEndian {
			data = append(data, 0xFF, 0xFE)
		} else {
			data = append(data, 0xFE, 0xFF)
		}
	}
	buf := make([]byte, 2)
	for _, r := range runes {
		order.PutUint16(buf, r)
		data = append(data, buf...)
	}
	return data
}

func decodeGB18030Bytes(data []byte) ([]byte, error) {
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func encodeGB18030String(content string) ([]byte, error) {
	encoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte(content))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func readFileSample(path string, limit int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	sample, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(sample)) > limit {
		return sample[:limit], false, nil
	}
	return sample, true, nil
}

func scoreDecodedText(content string) (float64, bool) {
	if content == "" {
		return 1, false
	}

	total := 0
	accepted := 0
	hasHan := false
	for _, r := range content {
		total++
		switch {
		case r == utf8.RuneError:
		case r == '\n' || r == '\r' || r == '\t':
			accepted++
		case unicode.IsPrint(r):
			accepted++
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if total == 0 {
		return 1, hasHan
	}
	return float64(accepted) / float64(total), hasHan
}

func containsNonASCIIByte(data []byte) bool {
	for _, b := range data {
		if b >= utf8.RuneSelf {
			return true
		}
	}
	return false
}

func isLikelyTextByte(b byte) bool {
	return b == '\n' || b == '\r' || b == '\t' || (b >= 0x20 && b <= 0x7E)
}
