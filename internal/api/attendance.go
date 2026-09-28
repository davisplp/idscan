package api

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"idscan/internal/attendance"
	"idscan/internal/pipeline"
)

// AttendanceResponse is the JSON shape returned by POST /api/attendance/scan.
type AttendanceResponse struct {
	// OK is true if OCR found at least one usable identifier (a phone
	// number, a card number, or both) — not specifically a phone number.
	// A card that scanned fine but matched no student is still OK:true —
	// see AttendanceStatus for that outcome instead.
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`

	RawOCRText string  `json:"raw_ocr_text,omitempty"`
	Confidence float64 `json:"confidence"`

	NormalizedPhone string `json:"normalized_phone,omitempty"`
	RawCardNumber   string `json:"card_number,omitempty"`

	// AttendanceStatus: "marked" | "already_marked" | "unmatched" | "" (if OK is false)
	AttendanceStatus string      `json:"attendance_status,omitempty"`
	MatchedVia       string      `json:"matched_via,omitempty"` // "card_number" | "phone"
	Student          *StudentDTO `json:"student,omitempty"`
	MarkedAt         string      `json:"marked_at,omitempty"`
	PreviousMarkedAt string      `json:"previous_marked_at,omitempty"`
}

// StudentDTO is the subset of student info the instructor's screen needs
// to see — never the whole students row.
type StudentDTO struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// NewAttendanceScanHandler builds the /api/attendance/scan handler. It
// takes the database handle as a constructor argument (rather than a
// package-level global) so main.go controls its lifecycle and the handler
// stays easy to test with a fake/mock DB later.
func NewAttendanceScanHandler(dbConn *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, AttendanceResponse{
				OK:      false,
				Message: "upload too large or malformed (max 10MB, field name must be 'image')",
			})
			return
		}

		file, _, err := r.FormFile("image")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, AttendanceResponse{
				OK:      false,
				Message: "missing 'image' file field",
			})
			return
		}
		defer file.Close()

		result, err := pipeline.Run(file)
		if err != nil {
			log.Printf("api: attendance pipeline failed: %v", err)
			writeJSON(w, http.StatusBadRequest, AttendanceResponse{
				OK:      false,
				Message: "could not process image: " + err.Error(),
			})
			return
		}
		log.Printf("api: attendance scan timing — decode=%dms preprocess=%dms ocr=%dms extract=%dms total=%dms",
			result.DecodeMS, result.PreprocessMS, result.OCRMS, result.ExtractMS, result.TotalMS)

		resp := AttendanceResponse{
			RawOCRText: result.OCRText,
			Confidence: result.Confidence,
		}

		// OK means "found at least one usable identifier" — a card number
		// alone, a phone number alone, or both. Bailing out here only when
		// NEITHER was found; otherwise we still attempt matching below,
		// since card-first matching is exactly for the case where the
		// phone field wasn't legible but the card number was (or vice
		// versa).
		if !result.Found && !result.CardFound {
			resp.OK = false
			resp.Message = "could not find a Contact No. or Card No. field — try retaking the photo with better lighting/focus"
			if _, err := attendance.RecordScan(r.Context(), dbConn, attendance.ScanInput{
				RawOCRText:    result.OCRText,
				Confidence:    result.Confidence,
				ScanningHubID: HubIDFromContext(r.Context()),
			}); err != nil {
				log.Printf("api: recording unmatched scan failed: %v", err)
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		resp.OK = true
		resp.NormalizedPhone = result.Normalized
		resp.RawCardNumber = result.RawCardNumber

		scanningHubID := HubIDFromContext(r.Context())

		dbStart := time.Now()
		att, err := attendance.RecordScan(r.Context(), dbConn, attendance.ScanInput{
			RawOCRText:      result.OCRText,
			RawPhoneMatch:   result.RawMatch,
			NormalizedPhone: result.Normalized,
			RawCardNumber:   result.RawCardNumber,
			Confidence:      result.Confidence,
			ScanningHubID:   scanningHubID,
		})
		dbMS := time.Since(dbStart).Milliseconds()
		log.Printf("api: attendance db timing — %dms", dbMS)
		if err != nil {
			log.Printf("api: attendance marking failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, AttendanceResponse{
				OK:      true,
				Message: "found an identifier but could not record attendance — try again",
			})
			return
		}

		resp.AttendanceStatus = string(att.Status)
		resp.MatchedVia = string(att.MatchedVia)

		switch att.Status {
		case attendance.StatusMarked:
			resp.Student = &StudentDTO{FirstName: att.Student.FirstName, LastName: att.Student.LastName}
			resp.MarkedAt = att.MarkedAt.Format(time.RFC3339)
			resp.Message = "attendance marked"
		case attendance.StatusAlreadyMarked:
			resp.Student = &StudentDTO{FirstName: att.Student.FirstName, LastName: att.Student.LastName}
			resp.PreviousMarkedAt = att.PreviousMarkedAt.Format(time.RFC3339)
			resp.Message = "already marked present today"
		case attendance.StatusUnmatched:
			resp.Message = "identifier read successfully, but no matching student was found — check the roster or retake the photo"
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
