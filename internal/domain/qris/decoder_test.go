package qris

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEMVCo_ValidStaticQR(t *testing.T) {
	// Build a minimal valid EMVCo payload
	qr := buildTLV(map[string]string{
		"00": "01",             // format indicator
		"01": "11",             // static
		"59": "TOKO SEJAHTERA", // merchant name
		"60": "JAKARTA",        // merchant city
		"58": "ID",             // country
	})

	decoded, err := ParseEMVCo(qr)
	require.NoError(t, err)
	assert.Equal(t, "TOKO SEJAHTERA", decoded.MerchantName)
	assert.Equal(t, "JAKARTA", decoded.MerchantCity)
	assert.True(t, decoded.Amount.IsZero())
	assert.False(t, decoded.IsAmountFixed)
}

func TestParseEMVCo_ValidDynamicQRWithAmount(t *testing.T) {
	qr := buildTLV(map[string]string{
		"00": "01",
		"01": "12",
		"54": "50000.00",
		"59": "WARUNG PADANG",
		"60": "BANDUNG",
		"58": "ID",
	})

	decoded, err := ParseEMVCo(qr)
	require.NoError(t, err)
	assert.Equal(t, "WARUNG PADANG", decoded.MerchantName)
	assert.Equal(t, "BANDUNG", decoded.MerchantCity)
	assert.True(t, decoded.Amount.Equal(decimal.NewFromInt(50000)))
	assert.True(t, decoded.IsAmountFixed)
}

func TestParseEMVCo_MissingMerchantName(t *testing.T) {
	qr := buildTLV(map[string]string{
		"00": "01",
		"60": "JAKARTA",
	})

	_, err := ParseEMVCo(qr)
	assert.Error(t, err)
}

func TestParseEMVCo_InvalidFormatIndicator(t *testing.T) {
	qr := buildTLV(map[string]string{
		"00": "99",
		"59": "TOKO",
		"60": "JAKARTA",
	})

	_, err := ParseEMVCo(qr)
	assert.Error(t, err)
}

func TestParseEMVCo_EmptyInput(t *testing.T) {
	_, err := ParseEMVCo("")
	assert.Error(t, err)
}

func TestParseEMVCo_TruncatedInput(t *testing.T) {
	_, err := ParseEMVCo("0002")
	assert.Error(t, err)
}

func TestParseEMVCo_NegativeAmount(t *testing.T) {
	qr := buildTLV(map[string]string{
		"00": "01",
		"54": "-100",
		"59": "TOKO",
		"60": "JAKARTA",
	})

	_, err := ParseEMVCo(qr)
	assert.Error(t, err)
}

func TestParseTLV_Valid(t *testing.T) {
	// "00" + "02" + "01" = tag 00, length 2, value "01"
	fields, err := parseTLV("000201")
	require.NoError(t, err)
	assert.Equal(t, "01", fields["00"])
}

func TestParseTLV_MultipleFields(t *testing.T) {
	raw := buildTLV(map[string]string{
		"00": "01",
		"59": "TOKO",
	})
	fields, err := parseTLV(raw)
	require.NoError(t, err)
	assert.Equal(t, "01", fields["00"])
	assert.Equal(t, "TOKO", fields["59"])
}

// buildTLV constructs TLV data in sorted tag order.
func buildTLV(fields map[string]string) string {
	// Sort tags for deterministic output
	tags := make([]string, 0, len(fields))
	for t := range fields {
		tags = append(tags, t)
	}
	// Simple sort
	for i := 0; i < len(tags)-1; i++ {
		for j := i + 1; j < len(tags); j++ {
			if tags[i] > tags[j] {
				tags[i], tags[j] = tags[j], tags[i]
			}
		}
	}

	result := ""
	for _, tag := range tags {
		val := fields[tag]
		result += tag + fmt.Sprintf("%02d", len(val)) + val
	}
	return result
}
