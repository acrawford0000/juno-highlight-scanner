package main

import (
	"fmt"
	"image/png"
	"math"
	"math/bits"
	"os"
	"sort"
)

const (
	frameWidth  = 680
	frameHeight = 340
	nameHeight  = 18
	victimStart = 452
	victimWidth = 193
)

type point struct{ x, y int }
type anchor struct{ x, y, contrast int }
type templatePixel struct {
	point
	value float64
}
type portraitPixel struct {
	point
	r, g, b int
}
type detector struct {
	anchors   []anchor
	name      []templatePixel
	portrait  []portraitPixel
	nameNorm  float64
	nameWidth int
}

type fingerprint struct {
	rows  [nameHeight][4]uint64
	count int
}

type detection struct {
	x, y   int
	score  float64
	victim fingerprint
}

func newDetector(path string) (*detector, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if config.Height != 38 || config.Width < 95 || config.Width > 300 {
		return nil, fmt.Errorf("reference PNG must be 38 pixels tall and 95–300 pixels wide")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	ref, err := png.Decode(file)
	if err != nil {
		return nil, err
	}
	// The crop starts just outside the blue box and ends after the main portrait.
	const nameX, nameY = 11, 10
	d := &detector{nameWidth: config.Width - 71}
	white := func(x, y int) int {
		r, g, b, _ := ref.At(x, y).RGBA()
		return int(min(r, g, b) >> 8)
	}
	var sum float64
	for y := 0; y < nameHeight; y++ {
		for x := 0; x < d.nameWidth; x++ {
			value := float64(white(nameX+x, nameY+y))
			d.name = append(d.name, templatePixel{point{x, y}, value})
			sum += value
		}
	}
	for i := range d.name {
		d.name[i].value -= sum / float64(len(d.name))
		d.nameNorm += d.name[i].value * d.name[i].value
	}
	for start := 0; start < d.nameWidth; start += 12 {
		best := anchor{}
		for x := start; x < min(start+12, d.nameWidth); x++ {
			for y := 2; y < nameHeight-2; y++ {
				bg := (white(nameX+x, nameY-4) + white(nameX+x, nameY+22)) / 2
				if contrast := white(nameX+x, nameY+y) - bg; contrast > best.contrast {
					best = anchor{x, y, contrast}
				}
			}
		}
		if best.contrast >= 55 {
			d.anchors = append(d.anchors, best)
		}
	}
	if len(d.anchors) < 2 {
		return nil, fmt.Errorf("reference PNG has too little visible lettering; choose a clear kill-feed row")
	}
	sort.Slice(d.anchors, func(i, j int) bool { return d.anchors[i].contrast > d.anchors[j].contrast })
	for y := -5; y <= 23; y += 3 {
		for x := d.nameWidth + 12; x <= d.nameWidth+46; x += 3 {
			r, g, b, _ := ref.At(nameX+x, nameY+y).RGBA()
			d.portrait = append(d.portrait, portraitPixel{point{x, y}, int(r >> 8), int(g >> 8), int(b >> 8)})
		}
	}
	return d, nil
}

func (d *detector) maxNameX() int {
	return min(477, frameWidth-d.nameWidth-50)
}

func (d *detector) find(rgb []byte, gray []byte) []detection {
	for i := range gray {
		j := i * 3
		gray[i] = min(rgb[j], rgb[j+1], rgb[j+2])
	}
	var found []detection
	for y := 32; y < frameHeight-28; y++ {
		if !hasRedBox(rgb, y) {
			continue
		}
		for x := 8; x < d.maxNameX(); x++ {
			if !d.nameAnchorsMatch(gray, x, y) || !d.hasBlueBox(rgb, x, y) {
				continue
			}
			score := d.nameScore(gray, x, y)
			if score < .63 {
				continue
			}
			if !d.portraitMatches(rgb, x, y, score) {
				continue
			}
			if n := len(found); n > 0 && y-found[n-1].y < 15 {
				if score > found[n-1].score {
					found[n-1] = detection{x, y, score, fingerprint{}}
				}
				continue
			}
			found = append(found, detection{x, y, score, fingerprint{}})
		}
	}
	for i := range found {
		found[i].victim = victimFingerprint(gray, found[i].y)
	}
	return found
}

func (d *detector) portraitMatches(rgb []byte, x, y int, nameScore float64) bool {
	// The compilation uses two full-size portrait alignments, two pixels apart.
	if d.portraitDifference(rgb, x, y) <= 33 || d.portraitDifference(rgb, x, y+2) <= 33 {
		return true
	}
	// Allow small independent shifts when the name matches strongly.
	if nameScore < .77 {
		return false
	}
	for dy := -2; dy <= 4; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if dx == 0 && (dy == 0 || dy == 2) {
				continue
			}
			if d.portraitDifference(rgb, x+dx, y+dy) <= 29 {
				return true
			}
		}
	}
	return false
}

func hasRedBox(rgb []byte, y int) bool {
	var top, bottom int
	for x := 605; x <= 646; x += 5 {
		a := ((y-4)*frameWidth + x) * 3
		b := ((y+22)*frameWidth + x) * 3
		top += int(rgb[a]) - int(rgb[a+1])
		bottom += int(rgb[b]) - int(rgb[b+1])
	}
	return top >= 9*24 && bottom >= 9*24
}

func (d *detector) hasBlueBox(rgb []byte, x, y int) bool {
	var sum, count int
	for dx := 0; dx < d.nameWidth-4; dx += 12 {
		i := ((y-4)*frameWidth + x + dx) * 3
		sum += int(rgb[i+2]) - int(rgb[i])
		count++
	}
	return sum >= count*23
}

func (d *detector) nameAnchorsMatch(gray []byte, x, y int) bool {
	missed := 0
	for _, a := range d.anchors {
		column := x + a.x
		bg := (int(gray[(y-4)*frameWidth+column]) + int(gray[(y+22)*frameWidth+column])) / 2
		if int(gray[(y+a.y)*frameWidth+column])-bg < a.contrast/4 {
			missed++
			if missed > 3 {
				return false
			}
		}
	}
	return true
}

func (d *detector) nameScore(gray []byte, x, y int) float64 {
	var dot, sum, square float64
	for _, p := range d.name {
		value := float64(gray[(y+p.y)*frameWidth+x+p.x])
		sum += value
		square += value * value
		dot += value * p.value
	}
	variance := square - sum*sum/float64(len(d.name))
	if variance <= 0 {
		return 0
	}
	return dot / math.Sqrt(d.nameNorm*variance)
}

func (d *detector) portraitDifference(rgb []byte, x, y int) float64 {
	// Center each color channel so changes in scene lighting behind the translucent
	// HUD have less influence on the comparison.
	var sum, square int
	var means [3]int
	for _, p := range d.portrait {
		i := ((y+p.y)*frameWidth + x + p.x) * 3
		diffs := [3]int{int(rgb[i]) - p.r, int(rgb[i+1]) - p.g, int(rgb[i+2]) - p.b}
		for c, diff := range diffs {
			means[c] += diff
			square += diff * diff
		}
	}
	n := float64(len(d.portrait))
	for _, m := range means {
		sum += m * m
	}
	return math.Sqrt(max(0, (float64(square)-float64(sum)/n)/(3*n)))
}

func victimFingerprint(gray []byte, y int) fingerprint {
	// The victim's name ends at the right margin. Use its light lettering above
	// the local red background, working left until the gap before the portrait.
	var columns [victimWidth]uint32
	for x := range columns {
		offset := victimStart + x
		bg := (int(gray[(y-4)*frameWidth+offset]) + int(gray[(y+22)*frameWidth+offset])) / 2
		for dy := range nameHeight {
			value := int(gray[(y+dy)*frameWidth+offset])
			if value >= 118 && value-bg >= 41 {
				columns[x] |= 1 << dy
			}
		}
		if bits.OnesCount32(columns[x]) < 2 {
			columns[x] = 0
		}
	}
	right := victimWidth - 1
	for right >= 0 && columns[right] == 0 {
		right--
	}
	var f fingerprint
	for x, gap := right, 0; x >= 0; x-- {
		if columns[x] == 0 {
			gap++
			if gap >= 9 {
				break
			}
			continue
		}
		gap = 0
		// Anchor to the HUD margin. A bright edge showing through the red
		// box can add pixels to the right of the name without moving it.
		col := victimWidth - 1 - x + 4
		for row := range nameHeight {
			if columns[x]&(1<<row) != 0 {
				f.rows[row][col/64] |= 1 << (col % 64)
				f.count++
			}
		}
	}
	return f
}

func similarity(a, b fingerprint) float64 {
	if min(a.count, b.count) < 20 {
		return 0
	}
	best := 0
	for dy := -2; dy <= 2; dy++ {
		for dx := 0; dx <= 4; dx++ {
			first, reverse := 0, 0
			for y := max(0, -dy); y < min(nameHeight, nameHeight-dy); y++ {
				for w := range a.rows[y] {
					av, bv := a.rows[y][w]<<dx, b.rows[y+dy][w]<<dx
					if w > 0 && dx > 0 {
						av |= a.rows[y][w-1] >> (64 - dx)
						bv |= b.rows[y+dy][w-1] >> (64 - dx)
					}
					first += bits.OnesCount64(av & b.rows[y+dy][w])
					reverse += bits.OnesCount64(bv & a.rows[y][w])
				}
			}
			best = max(best, first, reverse)
		}
	}
	return float64(2*best) / float64(a.count+b.count)
}
