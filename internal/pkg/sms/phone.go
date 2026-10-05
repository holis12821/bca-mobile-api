package sms

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPhone is returned for a number no Indonesian operator could route.
// It is returned before any HTTP call: an aggregator bills per accepted
// message, and a typo'd number is a message that can never arrive.
var ErrInvalidPhone = errors.New("sms: not a valid Indonesian mobile number")

// NormalizePhone converts an Indonesian mobile number to E.164 (+62…).
//
// The database stores what the nasabah typed — "0812-3456-7890", "62812…",
// "+62 812…" — but every aggregator wants one shape. Doing the conversion here,
// inside the gateway, means all three OTP call sites (personal-data, resend,
// regenerate) get it without each remembering to.
func NormalizePhone(raw string) (string, error) {
	national, err := nationalForm(raw)
	if err != nil {
		return "", err
	}
	return "+62" + national[1:], nil
}

// nationalForm returns the 0-prefixed national number ("08123456789").
func nationalForm(raw string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, raw)

	var national string
	switch {
	case strings.HasPrefix(digits, "62"):
		national = "0" + digits[2:]
	case strings.HasPrefix(digits, "0"):
		national = digits
	case strings.HasPrefix(digits, "8"):
		national = "0" + digits
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidPhone, maskForError(digits))
	}

	// 08 + operator block + subscriber: 10 to 13 digits in total. Anything
	// outside that is a landline, a typo, or a truncated paste.
	if len(national) < 10 || len(national) > 13 {
		return "", fmt.Errorf("%w: %q has %d digits, want 10-13", ErrInvalidPhone, maskForError(national), len(national))
	}
	if !strings.HasPrefix(national, "08") || national[2] == '0' {
		return "", fmt.Errorf("%w: %q is not a mobile prefix", ErrInvalidPhone, maskForError(national))
	}
	return national, nil
}

// operatorByPrefix maps the four-digit national prefix to the operator that
// owns the block.
//
// The block owner is what actually decides whether an SMS lands: an aggregator
// only hands the message over, and delivery problems are almost always
// per-operator. Carrying the name into the log is what turns "OTP sometimes
// never arrives" into "OTP never arrives on Indosat numbers", which is a
// question support can act on.
var operatorByPrefix = map[string]string{
	// Telkomsel (kartuHalo, simPATI, Kartu As, byU)
	"0811": "Telkomsel", "0812": "Telkomsel", "0813": "Telkomsel",
	"0821": "Telkomsel", "0822": "Telkomsel", "0823": "Telkomsel",
	"0851": "Telkomsel", "0852": "Telkomsel", "0853": "Telkomsel",

	// Indosat Ooredoo Hutchison (IM3)
	"0814": "Indosat", "0815": "Indosat", "0816": "Indosat",
	"0855": "Indosat", "0856": "Indosat", "0857": "Indosat", "0858": "Indosat",

	// Tri — merged into IOH but still a separate routing block
	"0895": "Tri", "0896": "Tri", "0897": "Tri", "0898": "Tri", "0899": "Tri",

	// XL Axiata
	"0817": "XL", "0818": "XL", "0819": "XL",
	"0859": "XL", "0877": "XL", "0878": "XL",

	// Axis (XL Axiata)
	"0831": "Axis", "0832": "Axis", "0833": "Axis", "0838": "Axis",

	// Smartfren
	"0881": "Smartfren", "0882": "Smartfren", "0883": "Smartfren",
	"0884": "Smartfren", "0885": "Smartfren", "0886": "Smartfren",
	"0887": "Smartfren", "0888": "Smartfren", "0889": "Smartfren",
}

// OperatorOf names the operator that owns the number's prefix, or "unknown".
//
// An unrecognised prefix is NOT an error: the regulator keeps allocating new
// blocks, and refusing a number because this table is a month out of date
// would block a real nasabah from registering. The send goes ahead; only the
// log says the block is unfamiliar.
func OperatorOf(phone string) string {
	national, err := nationalForm(phone)
	if err != nil {
		return "unknown"
	}
	if name, ok := operatorByPrefix[national[:4]]; ok {
		return name
	}
	return "unknown"
}

// maskForError keeps the middle of a rejected number out of the error string,
// which travels into logs. The prefix and last two digits are enough to see
// what shape was rejected.
func maskForError(digits string) string {
	if len(digits) <= 6 {
		return digits
	}
	return digits[:4] + "…" + digits[len(digits)-2:]
}
