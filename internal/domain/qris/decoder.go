package qris

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// EMVCo QR Code Data Object IDs (subset for portfolio purposes).
const (
	tagPayloadFormat     = "00"
	tagPOIMethod         = "01" // 11 = static, 12 = dynamic
	tagMerchantAccount26 = "26" // merchant account (ID-specific)
	tagMCC               = "52"
	tagCurrency          = "53"
	tagAmount            = "54"
	tagCountry           = "58"
	tagMerchantName      = "59"
	tagMerchantCity      = "60"
	tagCRC               = "63"
)

// ParseEMVCo parses a minimal EMVCo TLV QRIS payload.
// Only mandatory fields are required: format indicator, merchant name, merchant city.
// Amount may be absent for dynamic QR (user enters amount).
func ParseEMVCo(raw string) (*DecodedQRIS, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 10 {
		return nil, apperr.QRISInvalidPayload
	}

	fields, err := parseTLV(raw)
	if err != nil {
		return nil, apperr.QRISInvalidPayload
	}

	// Validate format indicator
	if fields[tagPayloadFormat] != "01" {
		return nil, apperr.QRISInvalidPayload
	}

	merchantName := fields[tagMerchantName]
	if merchantName == "" {
		return nil, apperr.QRISInvalidPayload
	}

	merchantCity := fields[tagMerchantCity]
	if merchantCity == "" {
		merchantCity = "UNKNOWN"
	}

	// Determine if amount is fixed
	poiMethod := fields[tagPOIMethod]
	isFixed := poiMethod == "12" // dynamic QR with fixed amount

	amount := decimal.Zero
	if amtStr := fields[tagAmount]; amtStr != "" {
		parsed, err := decimal.NewFromString(amtStr)
		if err != nil || parsed.IsNegative() {
			return nil, apperr.QRISInvalidPayload
		}
		amount = parsed
		isFixed = true
	}

	return &DecodedQRIS{
		MerchantName:  merchantName,
		MerchantCity:  merchantCity,
		Amount:        amount,
		IsAmountFixed: isFixed,
	}, nil
}

// parseTLV parses an EMVCo TLV string into tag→value map.
func parseTLV(data string) (map[string]string, error) {
	fields := make(map[string]string)
	i := 0
	for i < len(data) {
		if i+4 > len(data) {
			return nil, fmt.Errorf("truncated TLV at position %d", i)
		}
		tag := data[i : i+2]
		lengthStr := data[i+2 : i+4]
		length, err := strconv.Atoi(lengthStr)
		if err != nil {
			return nil, fmt.Errorf("invalid length at position %d", i+2)
		}
		i += 4
		if i+length > len(data) {
			return nil, fmt.Errorf("value overflow at tag %s", tag)
		}
		fields[tag] = data[i : i+length]
		i += length
	}
	return fields, nil
}