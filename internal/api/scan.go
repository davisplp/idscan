// Package api implements the HTTP handlers for the ID card scanning
// service.
package api

import (
	"encoding/json"
	"log"
	"net/http"

	"idscan/internal/pipeline"
)

// maxUploadBytes caps request body size to prevent a single huge upload
// from exhausting server memory. 10MB is generous for a compressed phone
// photo resized client-side (see frontend notes in README.md).
const maxUploadBytes = 10 << 20 // 10 MB

// ScanResponse is the JSON shape returned by POST /api/scan.
type ScanResponse struct {
	// OK is false when OCR ran but no confident phone number could be
	// extracted; the HTTP status is still 200 in that case, because the
	// request itself was handled correctly — the caller should look at OK
	// and Message to decide what to show the user (e.g. "please retake").
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`

	RawOCRText string `json:"raw_ocr_text,omitempty"`
	// Confidence is Tesseract's mean word confidence (0-100), or -1 if it
	// could not be computed. The frontend can use this to warn the user
	// even when a number was technically extracted, e.g. "we found a
	// number but the photo was blurry — please double check it."
	Confidence float64 `json:"confidence"`

	RawMatch        string `json:"raw_match,omitempty"`
	NormalizedPhone string `json:"normalized_phone,omitempty"`
	SourceLine      string `json:"source_line,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: failed to encode JSON response: %v", err)
	}
}

// ScanHandler handles POST /api/scan: an image upload (multipart form
// field "image") in, extracted-and-normalized phone number JSON out.
//
// This is intentionally synchronous and single-shot: preprocess -> OCR ->
// extract all happen within the request. At the traffic levels of a
// student-attendance scanning app this is simpler to reason about than a
// queue-based pipeline, and Tesseract on a single ID-card-sized image
// typically completes in well under a second. If scan volume grows enough
// that this becomes a bottleneck, the natural next step is to move OCR
// into a worker pool behind a queue rather than to optimize this handler.
func ScanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, ScanResponse{
			OK:      false,
			Message: "upload too large or malformed (max 10MB, field name must be 'image')",
		})
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ScanResponse{
			OK:      false,
			Message: "missing 'image' file field",
		})
		return
	}
	defer file.Close()

	result, err := pipeline.Run(file)
	if err != nil {
		log.Printf("api: pipeline failed: %v", err)
		writeJSON(w, http.StatusBadRequest, ScanResponse{
			OK:      false,
			Message: "could not process image: " + err.Error(),
		})
		return
	}
	log.Printf("api: scan timing — decode=%dms preprocess=%dms ocr=%dms extract=%dms total=%dms",
		result.DecodeMS, result.PreprocessMS, result.OCRMS, result.ExtractMS, result.TotalMS)

	resp := ScanResponse{
		RawOCRText: result.OCRText,
		Confidence: result.Confidence,
	}

	if !result.Found {
		resp.OK = false
		resp.Message = "could not find a Contact No. field — try retaking the photo with better lighting/focus"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.OK = true
	resp.RawMatch = result.RawMatch
	resp.NormalizedPhone = result.Normalized
	resp.SourceLine = result.SourceLine

	if result.Confidence >= 0 && result.Confidence < 60 {
		resp.Message = "low OCR confidence — please ask the user to confirm this number before saving"
	}

	writeJSON(w, http.StatusOK, resp)
}

// HealthHandler is a trivial liveness check for load balancers/uptime monitors.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
