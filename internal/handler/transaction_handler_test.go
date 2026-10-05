package handler

import (
	"testing"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// jakarta is the only timezone the mutation filter may reason in: the day
// boundary that matters to a nasabah is midnight in Jakarta, and computing it
// from the database's clock (CURRENT_DATE) moves "hari ini" by seven hours.
func jakarta(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatalf("load Asia/Jakarta: %v", err)
	}
	return loc
}

func TestResolvePeriod_NamedRanges(t *testing.T) {
	loc := jakarta(t)
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	firstThisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)

	cases := []struct {
		period string
		from   time.Time
		to     time.Time
	}{
		// Seven days INCLUDING today, which is why the offset is -6.
		{"LAST_7_DAYS", today.AddDate(0, 0, -6), today},
		{"LAST_30_DAYS", today.AddDate(0, 0, -29), today},
		{"LAST_90_DAYS", today.AddDate(0, 0, -89), today},
		{"THIS_MONTH", firstThisMonth, today},
		// The end of "bulan lalu" is the last day of that month, not the 1st of
		// this one: the range is compared as dates, so an exclusive-looking
		// boundary would pull today's neighbours into last month.
		{"LAST_MONTH", firstThisMonth.AddDate(0, -1, 0), firstThisMonth.AddDate(0, 0, -1)},
		// Lowercase is accepted; the enum is not a shibboleth.
		{"last_month", firstThisMonth.AddDate(0, -1, 0), firstThisMonth.AddDate(0, 0, -1)},
	}

	for _, tc := range cases {
		got, err := resolvePeriod(tc.period, "", "")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.period, err)
		}
		if got == nil {
			t.Fatalf("%s: expected a date range", tc.period)
		}
		if !got.From.Equal(tc.from) || !got.To.Equal(tc.to) {
			t.Fatalf("%s: got %s..%s, want %s..%s",
				tc.period, got.From, got.To, tc.from, tc.to)
		}
	}
}

// An unknown value used to fall through to a nil range, which means "no date
// filter": a typo in `period` answered 200 with the account's whole history and
// the screen presented it as the last seven days.
func TestResolvePeriod_UnknownValueRejected(t *testing.T) {
	_, err := resolvePeriod("LAST_YEAR", "", "")
	if err == nil {
		t.Fatal("an unknown period must be rejected, not silently ignored")
	}

	appErr := apperr.From(err)
	if appErr.Code != apperr.ValidationError.Code {
		t.Fatalf("expected VALIDATION_ERROR, got %s", appErr.Code)
	}

	details, ok := appErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("expected details naming the field, got %#v", appErr.Details)
	}
	if details["invalid_field"] != "period" {
		t.Fatalf("expected invalid_field=period, got %v", details["invalid_field"])
	}
	// The allowed list travels with the error so a client can fix the call
	// without opening the spec.
	if _, ok := details["allowed_values"]; !ok {
		t.Fatal("expected allowed_values in details")
	}
}

func TestResolvePeriod_Custom(t *testing.T) {
	loc := jakarta(t)

	got, err := resolvePeriod("CUSTOM", "2026-09-01", "2026-09-15")
	if err != nil {
		t.Fatalf("valid custom range: %v", err)
	}
	wantFrom := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	wantTo := time.Date(2026, 9, 15, 0, 0, 0, 0, loc)
	if !got.From.Equal(wantFrom) || !got.To.Equal(wantTo) {
		t.Fatalf("got %s..%s, want %s..%s", got.From, got.To, wantFrom, wantTo)
	}

	// CUSTOM without dates is a client bug, and answering the full history
	// would hide it.
	if _, err := resolvePeriod("CUSTOM", "", ""); err == nil {
		t.Fatal("CUSTOM without from/to must be rejected")
	}
	if _, err := resolvePeriod("CUSTOM", "01-09-2026", "15-09-2026"); err == nil {
		t.Fatal("a non-ISO date must be rejected")
	}
	if _, err := resolvePeriod("CUSTOM", "2026-09-15", "2026-09-01"); err == nil {
		t.Fatal("an inverted range must be rejected")
	}
}

// from+to without a period still filter: the app built custom ranges that way
// before the enum existed, and no period at all means no filter.
func TestResolvePeriod_BareFromTo(t *testing.T) {
	got, err := resolvePeriod("", "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatalf("bare from/to: %v", err)
	}
	if got == nil {
		t.Fatal("from+to alone should still produce a range")
	}

	got, err = resolvePeriod("", "", "")
	if err != nil {
		t.Fatalf("no filter at all: %v", err)
	}
	if got != nil {
		t.Fatalf("expected no date filter, got %s..%s", got.From, got.To)
	}
}
