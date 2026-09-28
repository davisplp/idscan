package extract

import "testing"

func TestNormalizeKenyanPhone(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0740133967", "+254740133967", false},
		{"0740 133 967", "+254740133967", false},
		{"254740133967", "+254740133967", false},
		{"+254740133967", "+254740133967", false},
		{"740133967", "+254740133967", false},
		{"0110123456", "+254110123456", false}, // 01x range
		{"123", "", true},                      // too short
		{"02012345678", "", true},              // landline-shaped, wrong length/prefix
	}

	for _, c := range cases {
		got, err := NormalizeKenyanPhone(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeKenyanPhone(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeKenyanPhone(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeKenyanPhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContactNumber_SameLine(t *testing.T) {
	ocr := "Card No.        : PLP-26-YOM-001-S9Z\n" +
		"Hub Assigned    : Yomlat\n" +
		"Contact No.     : 0740133967\n" +
		"Period Valid    : Sept 2026 - March 2027\n"

	res, err := ContactNumber(ocr)
	if err != nil {
		t.Fatalf("ContactNumber returned error: %v", err)
	}
	if res.Normalized != "+254740133967" {
		t.Errorf("got %q, want +254740133967", res.Normalized)
	}
}

func TestContactNumber_LabelValueSplitAcrossLines(t *testing.T) {
	// Simulates OCR occasionally putting the label and value on separate
	// lines when the colon doesn't render cleanly.
	ocr := "Contact No\n0740133967\n"

	res, err := ContactNumber(ocr)
	if err != nil {
		t.Fatalf("ContactNumber returned error: %v", err)
	}
	if res.Normalized != "+254740133967" {
		t.Errorf("got %q, want +254740133967", res.Normalized)
	}
}

func TestContactNumber_DoesNotMatchCardNumber(t *testing.T) {
	// The card number line contains digits but is NOT a phone number and
	// has no "Contact No" label anywhere near it. Extraction must not
	// mistake this for a phone number.
	ocr := "Card No.        : PLP-26-YOM-001-S9Z\n" +
		"Hub Assigned    : Yomlat\n"

	_, err := ContactNumber(ocr)
	if err == nil {
		t.Errorf("expected no match (no Contact No. label present), but got a result")
	}
}

func TestContactNumber_IdentificationNoVariant(t *testing.T) {
	// Some card templates use "Identification No." instead of "Card No.",
	// but should still find "Contact No." correctly.
	ocr := "Identification No. : 491631057\n" +
		"Hub Assigned : Garissa University\n" +
		"Contact No. : 0768032745\n"

	res, err := ContactNumber(ocr)
	if err != nil {
		t.Fatalf("ContactNumber returned error: %v", err)
	}
	if res.Normalized != "+254768032745" {
		t.Errorf("got %q, want +254768032745", res.Normalized)
	}
}

func TestCardNumber_DashedCodeTemplate(t *testing.T) {
	ocr := "Card No.        : PLP-26-YOM-001-S9Z\n" +
		"Hub Assigned    : Yomlat\n" +
		"Contact No.     : 0740133967\n"

	res, err := CardNumber(ocr)
	if err != nil {
		t.Fatalf("CardNumber returned error: %v", err)
	}
	if res.RawCardNumber != "PLP-26-YOM-001-S9Z" {
		t.Errorf("got %q, want PLP-26-YOM-001-S9Z", res.RawCardNumber)
	}
}

func TestCardNumber_PlainNumericIdentificationVariant(t *testing.T) {
	ocr := "Identification No. : 491631057\n" +
		"Hub Assigned : Garissa University\n" +
		"Contact No. : 0768032745\n"

	res, err := CardNumber(ocr)
	if err != nil {
		t.Fatalf("CardNumber returned error: %v", err)
	}
	if res.RawCardNumber != "491631057" {
		t.Errorf("got %q, want 491631057", res.RawCardNumber)
	}
}

func TestCardNumber_DoesNotMatchContactNumberLine(t *testing.T) {
	// The Contact No. line has no dashes and only a 9-10 digit run, which
	// is below CardNumber's 6-digit fallback threshold only if isolated
	// correctly by line -- this proves CardNumber never even looks at the
	// Contact No. line when a Card No. line is present elsewhere.
	ocr := "Card No.        : PLP-26-YOM-001-S9Z\n" +
		"Contact No.     : 0740133967\n"

	res, err := CardNumber(ocr)
	if err != nil {
		t.Fatalf("CardNumber returned error: %v", err)
	}
	if res.SourceLine != "Card No.        : PLP-26-YOM-001-S9Z" {
		t.Errorf("CardNumber matched the wrong line: %q", res.SourceLine)
	}
}

func TestContactNumber_DoesNotMatchCardNumberValue(t *testing.T) {
	// The reverse isolation check: ContactNumber must not pick up digits
	// from the Card No. line even though "26" and "001" are digit-shaped.
	ocr := "Card No.        : PLP-26-YOM-001-S9Z\n" +
		"Contact No.     : 0740133967\n"

	res, err := ContactNumber(ocr)
	if err != nil {
		t.Fatalf("ContactNumber returned error: %v", err)
	}
	if res.Normalized != "+254740133967" {
		t.Errorf("got %q, want +254740133967", res.Normalized)
	}
}
