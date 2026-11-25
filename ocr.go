package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/danlock/gogosseract"
	"github.com/tetratelabs/wazero"
)

//go:embed eng.traineddata
var englishOCR []byte

const ocrZoom = 2

type nameReader struct {
	player  string
	engine  *gogosseract.Tesseract
	mask    *image.Gray
	encoded bytes.Buffer
	encoder png.Encoder
}

func newNameReader(ctx context.Context, cache wazero.CompilationCache, player string) (*nameReader, error) {
	engine, err := gogosseract.New(ctx, gogosseract.Config{
		Language:     "eng",
		TrainingData: bytes.NewReader(englishOCR),
		Variables: map[string]string{
			"tessedit_pageseg_mode":   "11", // Sparse text across the kill feed.
			"tessedit_char_whitelist": "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
			"user_defined_dpi":        "150",
		},
		WASMCache: cache,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Tesseract OCR: %w", err)
	}
	return &nameReader{
		player:  player,
		engine:  engine,
		mask:    image.NewGray(image.Rect(0, 0, frameWidth*ocrZoom, frameHeight*ocrZoom)),
		encoder: png.Encoder{CompressionLevel: png.BestSpeed},
	}, nil
}

var ocrWord = regexp.MustCompile(`class=['"]ocrx_word['"][^>]*title=['"]bbox (\d+) (\d+) (\d+) (\d+)[^>]*>([A-Z0-9]+)</span>`)

func (r *nameReader) find(ctx context.Context, d *detector, rgb []byte) ([]detection, error) {
	if !hasFeedColors(rgb) || !r.prepareMask(rgb) {
		return nil, nil
	}
	r.encoded.Reset()
	if err := r.encoder.Encode(&r.encoded, r.mask); err != nil {
		return nil, err
	}
	if err := r.engine.LoadImage(ctx, &r.encoded, gogosseract.LoadImageOptions{}); err != nil {
		return nil, err
	}
	page, err := r.engine.GetHOCR(ctx, nil)
	if err != nil {
		return nil, err
	}
	var matches []detection
	for _, word := range ocrWord.FindAllStringSubmatch(page, -1) {
		text := word[5]
		exact := text == r.player
		if !exact && (len(r.player) < 8 || len(text) < 8 || !strings.HasPrefix(text, r.player[:4])) {
			continue
		}
		var bounds [4]float64
		for i := range bounds {
			value, _ := strconv.Atoi(word[i+1])
			bounds[i] = float64(value) / ocrZoom
		}
		if found, ok := verifyOCR(d, rgb, bounds, exact); ok {
			matches = append(matches, found)
		}
	}
	return matches, nil
}

func hasFeedColors(rgb []byte) bool {
	for y := 28; y < frameHeight-8; y += 4 {
		red := 0
		for x := 560; x < 665; x += 6 {
			i := (y*frameWidth + x) * 3
			if int(rgb[i])-int(rgb[i+1]) > 30 {
				red++
			}
		}
		if red < 10 {
			continue
		}
		blue := 0
		for x := 12; x < 560; x += 6 {
			i := (y*frameWidth + x) * 3
			if int(rgb[i+2])-int(rgb[i]) > 32 && int(rgb[i+1])-int(rgb[i]) > 15 {
				blue++
				if blue >= 12 {
					return true
				}
			}
		}
	}
	return false
}

func (r *nameReader) prepareMask(rgb []byte) bool {
	// Extract light lettering over blue and upscale it for the OCR model. A
	// vertical window keeps white strokes whose immediate pixels have no hue.
	for i := range r.mask.Pix {
		r.mask.Pix[i] = 255
	}
	const halfWindow = 9
	active := textRows(rgb)
	foreground := 0
	for x := 8; x < 545; x++ {
		var blue [frameHeight + 1]int
		var background [frameHeight + 1]int
		for y := 18; y < frameHeight; y++ {
			i := (y*frameWidth + x) * 3
			red := int(rgb[i])
			blue[y+1], background[y+1] = blue[y], background[y]
			if int(rgb[i+2])-red >= 24 && int(rgb[i+1])-red >= 12 {
				blue[y+1]++
				background[y+1] += red
			}
		}
		for y := 29; y < frameHeight-halfWindow; y++ {
			if !active[y] {
				continue
			}
			top, bottom := y-halfWindow, y+halfWindow+1
			count := blue[bottom] - blue[top]
			if count < 5 {
				continue
			}
			i := (y*frameWidth + x) * 3
			threshold := max(77, (background[bottom]-background[top])/count+24)
			if int(min(rgb[i], rgb[i+1], rgb[i+2])) < threshold {
				continue
			}
			foreground++
			for dy := range ocrZoom {
				start := (y*ocrZoom+dy)*r.mask.Stride + x*ocrZoom
				for dx := range ocrZoom {
					r.mask.Pix[start+dx] = 0
				}
			}
		}
	}
	return foreground >= 125
}

func textRows(rgb []byte) [frameHeight]bool {
	// Estimate text baselines from the height of the red victim boxes.
	var active [frameHeight]bool
	top := -1
	for y := 22; y < frameHeight-1; y++ {
		red := 0
		for x := 642; x <= 648; x += 2 {
			i := (y*frameWidth + x) * 3
			if int(rgb[i])-int(rgb[i+1]) >= 26 {
				red++
			}
		}
		if red >= 3 {
			if top < 0 {
				top = y
			}
			continue
		}
		height := y - top
		if top >= 0 && height >= 19 && height <= 60 {
			center := (top + y) / 2
			for row := center - height*2/9 - 1; row <= center+height*2/9+1; row++ {
				active[row] = true
			}
		}
		top = -1
	}
	return active
}

func verifyOCR(d *detector, rgb []byte, bounds [4]float64, exact bool) (detection, bool) {
	// Normalize a thin strip around the OCR hit. Anchor the strip to the right
	// of the screen and estimate HUD scale from the recognized name's width.
	const bandHeight, nameY = 40, 10
	rgbBand := make([]byte, frameWidth*bandHeight*3)
	grayBand := make([]byte, frameWidth*bandHeight)
	widthScale := (bounds[2] - bounds[0]) / float64(d.nameWidth-5)
	heightScale := (bounds[3] - bounds[1]) / 14
	for _, s := range []float64{widthScale, widthScale - .025, widthScale + .025, widthScale - .05, widthScale + .05,
		heightScale, heightScale - .04, heightScale + .04, heightScale - .08, heightScale + .08, 1, .8, 1.2} {
		if s < .6 || s > 1.6 {
			continue
		}
		x := int(math.Round(frameWidth+(bounds[0]-frameWidth)/s)) - 2
		if x < 8 || x >= d.maxNameX() {
			continue
		}
		for y := range bandHeight {
			sy := int(math.Round(bounds[1] + float64(y-nameY-2)*s))
			sy = max(0, min(frameHeight-1, sy))
			for col := range frameWidth {
				sx := int(math.Round(frameWidth + float64(col-frameWidth)*s))
				di := y*frameWidth + col
				if sx < 0 || sx >= frameWidth {
					clear(rgbBand[di*3 : di*3+3])
					grayBand[di] = 0
					continue
				}
				si := (sy*frameWidth + sx) * 3
				copy(rgbBand[di*3:di*3+3], rgb[si:si+3])
				grayBand[di] = min(rgb[si], rgb[si+1], rgb[si+2])
			}
		}
		for dy := -3; dy <= 3; dy++ {
			y := nameY + dy
			if !hasRedBox(rgbBand, y) {
				continue
			}
			for dx := -2; dx <= 2; dx++ {
				left := x + dx
				if !d.hasBlueBox(rgbBand, left, y) {
					continue
				}
				// Preserve the full name check when OCR misreads a glyph or
				// includes part of the neighboring portrait in the word.
				if !exact && d.nameScore(grayBand, left, y) < .54 {
					continue
				}
				if d.portraitMatches(rgbBand, left, y, 1) {
					return detection{int(bounds[0]), int(bounds[1]), 1, victimFingerprint(grayBand, y)}, true
				}
			}
		}
	}
	return detection{}, false
}

func matchWithOCR(ctx context.Context, reader *nameReader, d *detector, rgb, gray []byte, mode string) ([]detection, error) {
	if mode != "ocr" {
		calibrated := d.find(rgb, gray)
		if len(calibrated) > 0 || mode == "template" {
			return calibrated, nil
		}
	}
	found, err := reader.find(ctx, d, rgb)
	if err != nil {
		return nil, fmt.Errorf("read %s in kill feed: %w", reader.player, err)
	}
	return found, nil
}
