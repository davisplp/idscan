package extract

import "testing"

// Real OCR text as reconstructed by internal/ocr from an actual card scan
// (see conversation history), used as a realistic benchmark input rather
// than a synthetic minimal string.
const realOCRText = `2 Power Learn Project HIMillionDevs4Atrica
XQ
DIGITAL SKILLING AND EMPLOYABILITY PROGRAM
LEARNER : YOMLAT GATLUAK GENG
Card No. : PLP-26-YOM-001-S9Z
Hub Assigned : Yomlat
Contact No. : 0740133967
Period Valid Sept 2026 March 2027
-
:
MA
2
caer comp G0 er -
In Partnership with: PROSPECTS Microsoft`

func BenchmarkContactNumber(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = ContactNumber(realOCRText)
	}
}

func BenchmarkNormalizeKenyanPhone(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = NormalizeKenyanPhone("0740133967")
	}
}
