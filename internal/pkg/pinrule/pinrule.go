// Package pinrule holds the strength rules for six-character customer and agent
// credentials: onboarding access codes, transaction PINs, and the CS desk PIN.
//
// It lives in pkg/ rather than in a domain package because two unrelated domains
// enforce the same rules. The rules started inside internal/domain/onboarding as
// unexported helpers; internal/domain/cs cannot call them, and the import
// direction forbids one domain reaching into another. Copying them across was the
// obvious shortcut and the wrong one — two copies of a security rule drift, and a
// security rule that drifts does so silently.
package pinrule

import (
	"regexp"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// SequentialRunLength is how many consecutive characters (e.g. "234", "cba")
// are enough to call a credential sequential and reject it.
const SequentialRunLength = 3

var (
	accessCodeRegex = regexp.MustCompile(`^[a-zA-Z0-9]{6}$`)
	pinRegex        = regexp.MustCompile(`^[0-9]{6}$`)
)

// ValidateAccessCode checks access code rules:
// - Exactly 6 alphanumeric
// - Not all same character
// - Not sequential
func ValidateAccessCode(code string) error {
	if !accessCodeRegex.MatchString(code) {
		return apperr.CredWeakAccessCode
	}
	lower := strings.ToLower(code)
	if AllSameChar(lower) {
		return apperr.CredWeakAccessCode
	}
	if Sequential(lower) {
		return apperr.CredWeakAccessCode
	}
	return nil
}

// ValidatePIN checks PIN rules:
// - Exactly 6 digits
// - Not all same digit
// - Not sequential
//
// Used by both the customer transaction PIN and the CS desk PIN. The desk PIN is
// a weaker credential by design — it only lifts the curtain over a shift that is
// already open — but "weaker" means it unlocks less, not that 123456 is allowed.
func ValidatePIN(pin string) error {
	if !pinRegex.MatchString(pin) {
		return apperr.CredWeakPIN
	}
	if AllSameChar(pin) {
		return apperr.CredWeakPIN
	}
	if Sequential(pin) {
		return apperr.CredWeakPIN
	}
	return nil
}

// AllSameChar checks if all characters are the same (e.g. "aaaaaa", "111111").
func AllSameChar(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// Sequential reports whether the string contains a run of at least
// SequentialRunLength characters stepping by ±1 — "123456", "abcdef", "654321",
// and also "ab1234", where the run sits inside an otherwise random string.
//
// runLength counts characters, not gaps: a pair one apart is already a run of
// two. An earlier version seeded the counter at 1 for a pair, so it only fired at
// four characters while claiming three in its comment.
func Sequential(s string) bool {
	if len(s) < SequentialRunLength {
		return false
	}

	runLength := 1
	for i := 1; i < len(s); i++ {
		diff := int(s[i]) - int(s[i-1])
		if diff == 1 || diff == -1 {
			if i >= 2 && diff == int(s[i-1])-int(s[i-2]) {
				runLength++ // same direction as the previous step: the run continues
			} else {
				runLength = 2 // a new run starts at this pair
			}
			if runLength >= SequentialRunLength {
				return true
			}
			continue
		}
		runLength = 1
	}

	return false
}
