// Package pipeline runs the shared upload -> preprocess -> OCR -> extract
// sequence used by every scanning endpoint. It exists so /api/scan and
// /api/attendance/scan don't duplicate this logic — attendance marking is
// "the scan pipeline, plus a database match", not a different pipeline.
package pipeline

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"time"

	"idscan/internal/extract"
	"idscan/internal/ocr"
	"idscan/internal/preprocess"
)

// Result holds everything an API handler needs to build its response,
// whether or not a Contact No. field was actually found.
type Result struct {
	OCRText    string
	Confidence float64

	// Found is false when OCR ran successfully but no Contact No. field
	// could be located — this is an expected outcome (e.g. a blurry photo,
	// or the back of the card), not an error.
	Found      bool
	RawMatch   string
	Normalized string
	SourceLine string

	// CardFound is the same idea for the card's own identifier field
	// ("Card No." / "Identification No."). Independent of Found: a scan
	// can locate one field without the other.
	CardFound      bool
	RawCardNumber  string
	CardSourceLine string

	// Timing breakdown in milliseconds, logged by callers so per-scan
	// latency stays visible in production rather than only known from a
	// one-off benchmark. See internal/ocr's RecognizeGray doc comment for
	// why OCRMS is the dominant cost and what was done to cut it down.
	DecodeMS     int64
	PreprocessMS int64
	OCRMS        int64
	ExtractMS    int64
	TotalMS      int64
}

// Run decodes an uploaded image, preprocesses it, runs OCR, and extracts
// the contact number. It returns an error only for hard failures (bad
// image data, OCR engine failure) — "no phone number found" is reported
// via Result.Found, not an error, so callers don't need two different
// failure-handling paths for two different kinds of "no result".
func Run(r io.Reader) (Result, error) {
	totalStart := time.Now()

	t0 := time.Now()
	srcImg, _, err := image.Decode(r)
	decodeMS := time.Since(t0).Milliseconds()
	if err != nil {
		return Result{}, fmt.Errorf("pipeline: decoding image: %w", err)
	}

	t1 := time.Now()
	gray, err := preprocess.Prepare(srcImg, preprocess.DefaultOptions())
	preprocessMS := time.Since(t1).Milliseconds()
	if err != nil {
		return Result{}, fmt.Errorf("pipeline: preprocessing: %w", err)
	}

	engine := ocr.New()
	defer engine.Close()

	t2 := time.Now()
	ocrResult, err := engine.RecognizeGray(gray)
	ocrMS := time.Since(t2).Milliseconds()
	if err != nil {
		return Result{}, fmt.Errorf("pipeline: OCR: %w", err)
	}

	res := Result{
		OCRText:      ocrResult.Text,
		Confidence:   ocrResult.MeanConfidence,
		DecodeMS:     decodeMS,
		PreprocessMS: preprocessMS,
		OCRMS:        ocrMS,
	}

	t3 := time.Now()
	contact, contactErr := extract.ContactNumber(ocrResult.Text)
	card, cardErr := extract.CardNumber(ocrResult.Text)
	res.ExtractMS = time.Since(t3).Milliseconds()
	res.TotalMS = time.Since(totalStart).Milliseconds()

	if cardErr == nil {
		res.CardFound = true
		res.RawCardNumber = card.RawCardNumber
		res.CardSourceLine = card.SourceLine
	}

	if contactErr != nil {
		return res, nil // Found stays false; this is not an error condition.
	}

	res.Found = true
	res.RawMatch = contact.RawMatch
	res.Normalized = contact.Normalized
	res.SourceLine = contact.SourceLine
	return res, nil
}
