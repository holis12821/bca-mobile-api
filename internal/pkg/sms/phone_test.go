package sms

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"national zero prefix", "081234567890", "+6281234567890"},
		{"country code without plus", "6281234567890", "+6281234567890"},
		{"e164 already", "+6281234567890", "+6281234567890"},
		{"bare subscriber", "81234567890", "+6281234567890"},
		{"dashes and spaces", "0812-3456-7890", "+6281234567890"},
		{"spaces and parens", "+62 (812) 3456 7890", "+6281234567890"},
		{"shortest accepted", "0811234567", "+62811234567"},
		{"longest accepted", "0812345678901", "+62812345678901"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizePhone(tc.in)
			if err != nil {
				t.Fatalf("NormalizePhone(%q) returned error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizePhone(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePhoneRejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"too short", "0812345"},
		{"too long", "08123456789012"},
		{"landline jakarta", "0215551234"},
		{"not a mobile block", "08012345678"},
		{"letters only", "bukan-nomor"},
		{"foreign number", "+14155552671"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizePhone(tc.in)
			if err == nil {
				t.Fatalf("NormalizePhone(%q) = %q, want an error", tc.in, got)
			}
			if !errors.Is(err, ErrInvalidPhone) {
				t.Fatalf("NormalizePhone(%q) error = %v, want ErrInvalidPhone", tc.in, err)
			}
		})
	}
}

// A rejected number travels into logs through the error string, so the middle
// digits must not be in it.
func TestNormalizePhoneErrorMasksTheNumber(t *testing.T) {
	t.Parallel()

	const full = "08123456789012" // too long, so it is rejected
	_, err := NormalizePhone(full)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); strings.Contains(got, "3456789") {
		t.Fatalf("error leaked the subscriber digits: %q", got)
	}
}

func TestOperatorOf(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"081234567890":   "Telkomsel",
		"+6285298765432": "Telkomsel",
		"085712345678":   "Indosat",
		"6281412345678":  "Indosat",
		"089612345678":   "Tri",
		"081712345678":   "XL",
		"083812345678":   "Axis",
		"088112345678":   "Smartfren",
	}

	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			if got := OperatorOf(in); got != want {
				t.Fatalf("OperatorOf(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// An unallocated-looking prefix must not be an error: the regulator keeps
// issuing new blocks, and this table will be out of date before the service is.
func TestOperatorOfUnknownPrefixIsNotFatal(t *testing.T) {
	t.Parallel()

	if got := OperatorOf("086912345678"); got != "unknown" {
		t.Fatalf("OperatorOf = %q, want \"unknown\"", got)
	}
	if _, err := NormalizePhone("086912345678"); err != nil {
		t.Fatalf("an unfamiliar prefix must still normalise, got %v", err)
	}
}
