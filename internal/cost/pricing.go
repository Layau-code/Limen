package cost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	nanoUSDPerUSD  = int64(1_000_000_000)
	nanoUSDPerUnit = int64(1_000_000)
	maxInt64       = int64(1<<63 - 1)
)

// Pricing 保存每百万 Token 的定点价格，单位是十亿分之一美元。
type Pricing struct {
	InputPerMillionNanoUSD  int64 `json:"input_per_million_nano_usd"`
	OutputPerMillionNanoUSD int64 `json:"output_per_million_nano_usd"`
}

// ParseUSD 将非负十进制美元字符串解析为纳美元定点整数。
func ParseUSD(raw string) (int64, error) {
	return parseNanoUSD(raw)
}

type pricingDocument struct {
	InputPerMillionUSD  string `json:"input_per_million_usd"`
	OutputPerMillionUSD string `json:"output_per_million_usd"`
}

// MarshalJSON 使用公开美元字符串格式编码价格，保证配置和决策快照可逆。
func (pricing Pricing) MarshalJSON() ([]byte, error) {
	if pricing.InputPerMillionNanoUSD < 0 || pricing.OutputPerMillionNanoUSD < 0 {
		return nil, errors.New("pricing must not be negative")
	}
	return json.Marshal(pricingDocument{
		InputPerMillionUSD:  FormatUSD(pricing.InputPerMillionNanoUSD),
		OutputPerMillionUSD: FormatUSD(pricing.OutputPerMillionNanoUSD),
	})
}

// UnmarshalJSON 将配置文件中的十进制价格解析为定点整数。
func (pricing *Pricing) UnmarshalJSON(data []byte) error {
	var document pricingDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode pricing: %w", err)
	}
	input, err := parseNanoUSD(document.InputPerMillionUSD)
	if err != nil {
		return fmt.Errorf("input_per_million_usd: %w", err)
	}
	output, err := parseNanoUSD(document.OutputPerMillionUSD)
	if err != nil {
		return fmt.Errorf("output_per_million_usd: %w", err)
	}
	pricing.InputPerMillionNanoUSD = input
	pricing.OutputPerMillionNanoUSD = output
	return nil
}

// Cost 根据输入和输出 Token 计算本次调用的定点成本。
func (pricing Pricing) Cost(inputTokens, outputTokens int64) (int64, error) {
	inputCost, err := roundedCost(inputTokens, pricing.InputPerMillionNanoUSD)
	if err != nil {
		return 0, fmt.Errorf("input cost: %w", err)
	}
	outputCost, err := roundedCost(outputTokens, pricing.OutputPerMillionNanoUSD)
	if err != nil {
		return 0, fmt.Errorf("output cost: %w", err)
	}
	if inputCost > maxInt64-outputCost {
		return 0, errors.New("cost overflows int64")
	}
	return inputCost + outputCost, nil
}

// FormatUSD 将定点成本格式化为普通十进制美元字符串。
func FormatUSD(nanoUSD int64) string {
	if nanoUSD < 0 {
		return ""
	}
	whole := strconv.FormatInt(nanoUSD/nanoUSDPerUSD, 10)
	fraction := strconv.FormatInt(nanoUSD%nanoUSDPerUSD, 10)
	if fraction == "0" {
		return whole
	}
	fraction = strings.Repeat("0", 9-len(fraction)) + fraction
	fraction = strings.TrimRight(fraction, "0")
	return whole + "." + fraction
}

func parseNanoUSD(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if raw == "" || len(parts) > 2 || parts[0] == "" || !decimalDigits(parts[0]) {
		return 0, errors.New("must be a non-negative decimal with at most 9 fractional digits")
	}
	if len(parts) == 2 && (parts[1] == "" || !decimalDigits(parts[1]) || len(parts[1]) > 9) {
		return 0, errors.New("must be a non-negative decimal with at most 9 fractional digits")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > maxInt64/nanoUSDPerUSD {
		return 0, errors.New("value is too large")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	fraction += strings.Repeat("0", 9-len(fraction))
	fractionValue, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil || whole == maxInt64/nanoUSDPerUSD && fractionValue > maxInt64%nanoUSDPerUSD {
		return 0, errors.New("value is too large")
	}
	return whole*nanoUSDPerUSD + fractionValue, nil
}

func decimalDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func roundedCost(tokens, price int64) (int64, error) {
	if tokens < 0 || price < 0 {
		return 0, errors.New("tokens and price must be non-negative")
	}
	if tokens == 0 || price == 0 {
		return 0, nil
	}
	if price > maxInt64/tokens {
		return 0, errors.New("cost overflows int64")
	}
	product := tokens * price
	if product > maxInt64-(nanoUSDPerUnit/2) {
		return 0, errors.New("cost overflows int64")
	}
	return (product + nanoUSDPerUnit/2) / nanoUSDPerUnit, nil
}
