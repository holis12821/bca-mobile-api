package onboarding

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var nikRegex = regexp.MustCompile(`^[0-9]{16}$`)

// ParseKTPFromText extracts KTP fields from raw OCR text.
// This is a best-effort parser for Indonesian e-KTP layout.
func ParseKTPFromText(raw string) KTPData {
	lines := strings.Split(raw, "\n")
	data := KTPData{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		upper := strings.ToUpper(line)

		// Try to extract key-value pairs with ":" separator
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			// Fallback: look for NIK pattern anywhere in the line
			if data.NIK == "" {
				if nik := extractNIK(line); nik != "" {
					data.NIK = nik
				}
			}
			continue
		}

		key := strings.TrimSpace(strings.ToUpper(parts[0]))
		val := strings.TrimSpace(parts[1])

		switch {
		case strings.Contains(key, "NIK"):
			if nik := extractNIK(val); nik != "" {
				data.NIK = nik
			}
		case strings.Contains(key, "NAMA"):
			data.NamaLengkap = val
		case strings.Contains(key, "TEMPAT") && strings.Contains(key, "LAHIR"):
			// "Tempat/Tgl Lahir : Jakarta, 21-04-1995"
			tlParts := strings.SplitN(val, ",", 2)
			data.TempatLahir = strings.TrimSpace(tlParts[0])
			if len(tlParts) == 2 {
				data.TanggalLahir = normalizeDateString(strings.TrimSpace(tlParts[1]))
			}
		case strings.Contains(key, "JENIS KELAMIN") || strings.Contains(key, "JK"):
			if strings.Contains(upper, "PEREMPUAN") {
				data.JenisKelamin = "PEREMPUAN"
			} else {
				data.JenisKelamin = "LAKI_LAKI"
			}
		case strings.Contains(key, "ALAMAT"):
			data.Alamat = val
		case strings.Contains(key, "RT") && strings.Contains(key, "RW"):
			data.RTRW = val
		case strings.Contains(key, "KEL") && !strings.Contains(key, "KEC"):
			data.Kelurahan = val
		case strings.Contains(key, "KEC"):
			data.Kecamatan = val
		case strings.Contains(key, "KOTA") || strings.Contains(key, "KABUPATEN"):
			data.Kota = val
		case strings.Contains(key, "PROVINSI"):
			data.Provinsi = val
		case strings.Contains(key, "AGAMA"):
			data.Agama = val
		case strings.Contains(key, "STATUS PERKAWINAN") || strings.Contains(key, "KAWIN"):
			data.StatusPerkawinan = val
		}
	}

	// Infer gender from NIK if not explicitly found
	if data.JenisKelamin == "" && data.NIK != "" {
		data.JenisKelamin = GenderFromNIK(data.NIK)
	}

	return data
}

// extractNIK finds a 16-digit sequence in a string.
func extractNIK(s string) string {
	// Remove spaces and dots that OCR might insert
	cleaned := strings.ReplaceAll(s, " ", "")
	cleaned = strings.ReplaceAll(cleaned, ".", "")

	if nikRegex.MatchString(cleaned) {
		return cleaned
	}

	// Try to find 16 consecutive digits in the string
	re := regexp.MustCompile(`[0-9]{16}`)
	match := re.FindString(cleaned)
	return match
}

// ValidateNIK checks the NIK structure: PPKKCC-DDMMYY-NNNN.
// PP = province (01-94), KK = city, CC = district, DD = date (female +40), MM = month, YY = year.
func ValidateNIK(nik string) error {
	if !nikRegex.MatchString(nik) {
		return fmt.Errorf("NIK must be exactly 16 digits")
	}

	// Province code (first 2 digits): 11-94
	provinceCode, _ := strconv.Atoi(nik[:2])
	if provinceCode < 11 || provinceCode > 94 {
		return fmt.Errorf("invalid province code: %d", provinceCode)
	}

	// Date of birth (digits 6-7): 01-31 for male, 41-71 for female
	dd, _ := strconv.Atoi(nik[6:8])
	if dd == 0 || (dd > 31 && dd < 41) || dd > 71 {
		return fmt.Errorf("invalid birth date in NIK: %d", dd)
	}

	// Month (digits 8-9): 01-12
	mm, _ := strconv.Atoi(nik[8:10])
	if mm < 1 || mm > 12 {
		return fmt.Errorf("invalid birth month in NIK: %d", mm)
	}

	return nil
}

// GenderFromNIK extracts gender from the NIK date field.
// DD > 40 means female (date + 40).
func GenderFromNIK(nik string) string {
	if len(nik) < 8 {
		return ""
	}
	dd, err := strconv.Atoi(nik[6:8])
	if err != nil {
		return ""
	}
	if dd > 40 {
		return "PEREMPUAN"
	}
	return "LAKI_LAKI"
}

// normalizeDateString tries to convert common date formats to YYYY-MM-DD.
func normalizeDateString(s string) string {
	s = strings.TrimSpace(s)
	// Already ISO format
	if len(s) == 10 && s[4] == '-' {
		return s
	}
	// DD-MM-YYYY
	if len(s) == 10 && s[2] == '-' {
		return s[6:10] + "-" + s[3:5] + "-" + s[0:2]
	}
	return s
}
