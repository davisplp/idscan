// Package ocr wraps Tesseract (via gosseract) with the settings that work
// best for short, structured ID-card text rather than full-page documents.
package ocr

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"sort"
	"strings"

	"github.com/otiai10/gosseract/v2"
)

// Result holds what OCR produced, plus a rough confidence signal so callers
// can decide whether to trust the extraction or ask the user to retake the
// photo.
type Result struct {
	Text           string
	MeanConfidence float64 // 0-100, Tesseract's own average word confidence
}

// Engine wraps a gosseract client. Engines are not safe for concurrent use
// (the underlying Tesseract client isn't), so callers should create one per
// request rather than sharing a single instance across goroutines.
type Engine struct {
	client *gosseract.Client
}

// New creates an OCR engine configured for short structured text (ID cards)
// rather than dense prose.
func New() *Engine {
	client := gosseract.NewClient()

	// PSM 6 = "assume a single uniform block of text". ID cards are a small
	// block of short lines, not a full page — this consistently outperforms
	// Tesseract's default page-layout-analysis mode on cards.
	client.SetPageSegMode(gosseract.PSM_SINGLE_BLOCK)

	// Restrict the character whitelist loosely: ID cards are mostly
	// alphanumerics, spaces, colons, dashes and periods. Excluding stray
	// symbols reduces noise from things like the card's border or logos
	// bleeding into misrecognized punctuation.
	_ = client.SetWhitelist("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789:.-/ ")

	return &Engine{client: client}
}

// Close releases the underlying Tesseract client. Callers must call this
// when done (typically via defer) to avoid leaking C resources.
func (e *Engine) Close() error {
	return e.client.Close()
}

// RecognizeGray runs OCR against a grayscale image already produced by the
// preprocess package.
//
// PERFORMANCE NOTE: this makes exactly one call into Tesseract
// (GetBoundingBoxes), not two. An earlier version called client.Text() for
// the text and client.GetBoundingBoxes() separately for confidence —
// benchmarking against real scans showed each call independently re-runs
// Tesseract's full recognition pass (gosseract/Tesseract does not cache
// the result between separate Get*/Text calls), so calling both cost
// roughly 2x a single real OCR pass for no benefit. GetBoundingBoxes
// already returns each recognized word's text, so the full text is
// reconstructed locally from the same single call instead — this measured
// ~2.3s -> ~0.9s per scan on a real phone photo, while keeping the
// confidence score (rather than dropping it to save time).
func (e *Engine) RecognizeGray(img *image.Gray) (Result, error) {
	tmpFile, err := os.CreateTemp("", "idscan-ocr-*.png")
	if err != nil {
		return Result{}, fmt.Errorf("ocr: creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := png.Encode(tmpFile, img); err != nil {
		tmpFile.Close()
		return Result{}, fmt.Errorf("ocr: encoding temp png: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return Result{}, fmt.Errorf("ocr: closing temp file: %w", err)
	}

	if err := e.client.SetImage(tmpPath); err != nil {
		return Result{}, fmt.Errorf("ocr: setting image: %w", err)
	}

	boxes, err := e.client.GetBoundingBoxes(gosseract.RIL_WORD)
	if err != nil {
		return Result{}, fmt.Errorf("ocr: recognizing text: %w", err)
	}
	if len(boxes) == 0 {
		return Result{Text: "", MeanConfidence: -1}, nil
	}

	var sum float64
	for _, b := range boxes {
		sum += b.Confidence
	}
	meanConfidence := sum / float64(len(boxes))

	return Result{
		Text:           reconstructLines(boxes),
		MeanConfidence: meanConfidence,
	}, nil
}

// reconstructLines rebuilds line-structured text from individual word
// bounding boxes, since gosseract's GetBoundingBoxes does not reliably
// populate BlockNum/ParNum/LineNum (observed to always be 0 in testing) —
// but the Y coordinates it returns are accurate, so words are clustered
// into lines by Y position instead. This matters beyond cosmetics:
// internal/extract's label-anchored matching depends on the "Contact No."
// line being distinct from the "Card No." line, so a false merge here
// could make extraction pick up the wrong digits.
func reconstructLines(boxes []gosseract.BoundingBox) string {
	sorted := make([]gosseract.BoundingBox, len(boxes))
	copy(sorted, boxes)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Box.Min.Y != sorted[j].Box.Min.Y {
			return sorted[i].Box.Min.Y < sorted[j].Box.Min.Y
		}
		return sorted[i].Box.Min.X < sorted[j].Box.Min.X
	})

	// Words on the same printed line land within a few pixels of each
	// other vertically; this tolerance was tuned against a real card photo
	// at the preprocess package's default upscale size (long edge 1600px).
	const lineTolerance = 15

	type line struct {
		y     int
		words []gosseract.BoundingBox
	}
	var lines []line
	for _, b := range sorted {
		placed := false
		for i := range lines {
			if abs(lines[i].y-b.Box.Min.Y) <= lineTolerance {
				lines[i].words = append(lines[i].words, b)
				placed = true
				break
			}
		}
		if !placed {
			lines = append(lines, line{y: b.Box.Min.Y, words: []gosseract.BoundingBox{b}})
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].y < lines[j].y })

	var sb strings.Builder
	for i, l := range lines {
		sort.SliceStable(l.words, func(a, c int) bool { return l.words[a].Box.Min.X < l.words[c].Box.Min.X })
		if i > 0 {
			sb.WriteString("\n")
		}
		for j, w := range l.words {
			if j > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(w.Word)
		}
	}
	return sb.String()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
