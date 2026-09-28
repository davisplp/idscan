// Package preprocess turns a raw phone-camera photo of an ID card into an
// image that OCR can read reliably: grayscale, contrast-stretched, and
// upscaled if the source image is small.
//
// NOTE on scope: full perspective correction (finding the card's four
// corners and warping them flat) is the single highest-impact step for
// phone-camera captures, but it needs OpenCV (gocv), which is a heavy
// cgo + system-library dependency. This package deliberately sticks to
// stdlib-only operations so the service has zero fragile external image
// dependencies. Perspective correction is the natural next upgrade —
// see README.md "Next steps".
package preprocess

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
)

// Options controls how an input image is prepared for OCR.
type Options struct {
	// MinLongEdge is the minimum size (px) of the longer image dimension.
	// Small phone thumbnails are upscaled to at least this before OCR,
	// since Tesseract performs poorly on small text.
	MinLongEdge int

	// ContrastStretch, when true, stretches the grayscale histogram so the
	// darkest pixel becomes black and the lightest becomes white. This
	// helps with the low-contrast, slightly washed-out look common in
	// phone photos taken under indoor lighting.
	ContrastStretch bool
}

// DefaultOptions returns sensible defaults for ID card phone photos.
func DefaultOptions() Options {
	return Options{
		MinLongEdge:     1600,
		ContrastStretch: true,
	}
}

// Prepare runs the preprocessing pipeline and returns a new grayscale image
// ready to hand to the OCR engine.
func Prepare(src image.Image, opts Options) (*image.Gray, error) {
	if src == nil {
		return nil, fmt.Errorf("preprocess: nil source image")
	}

	gray := toGray(src)

	if opts.MinLongEdge > 0 {
		gray = upscaleIfSmall(gray, opts.MinLongEdge)
	}

	if opts.ContrastStretch {
		stretchContrast(gray)
	}

	return gray, nil
}

// toGray converts any image.Image to grayscale using the standard
// luminance-weighted conversion (image/color already does this correctly
// via color.GrayModel, which accounts for human perceived brightness).
func toGray(src image.Image) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// upscaleIfSmall performs a simple nearest-neighbor upscale if the image's
// longer edge is below minLongEdge. Nearest-neighbor is intentionally
// chosen over a smoother filter: OCR engines generally do better with
// crisp, blocky edges on upscaled text than with a blurred bicubic
// interpolation, since blur softens the stroke edges Tesseract relies on.
func upscaleIfSmall(src *image.Gray, minLongEdge int) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	longEdge := w
	if h > longEdge {
		longEdge = h
	}
	if longEdge >= minLongEdge || longEdge == 0 {
		return src
	}

	scale := float64(minLongEdge) / float64(longEdge)
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)

	dst := image.NewGray(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		srcY := int(float64(y) / scale)
		for x := 0; x < newW; x++ {
			srcX := int(float64(x) / scale)
			dst.SetGray(x, y, src.GrayAt(b.Min.X+srcX, b.Min.Y+srcY))
		}
	}
	return dst
}

// stretchContrast performs a linear histogram stretch in place: the
// darkest pixel value in the image is mapped to 0 and the lightest to 255,
// with everything else scaled linearly between. This is cheap, has no
// external dependencies, and meaningfully helps OCR on photos with mild
// glare or dim, uneven lighting. It intentionally does NOT attempt
// adaptive/local thresholding (which handles harsh glare or shadows across
// a single card much better) — that's a good candidate to add if accuracy
// testing shows glare is a recurring failure mode.
func stretchContrast(img *image.Gray) {
	b := img.Bounds()
	min, max := uint8(255), uint8(0)

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			v := img.GrayAt(x, y).Y
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
	}

	if max <= min {
		return // flat image (e.g. blank frame); nothing to stretch
	}

	rangeF := float64(max - min)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			v := img.GrayAt(x, y).Y
			stretched := uint8((float64(v-min) / rangeF) * 255)
			img.SetGray(x, y, color.Gray{Y: stretched})
		}
	}
}
