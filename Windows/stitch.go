package main

import (
	"bytes"
	"image"
	"sort"
)

const samples = 64

type frame struct {
	image *image.RGBA
	gray  []byte
}

func newFrame(im *image.RGBA) frame {
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	gray := make([]byte, samples*h)
	margin := w / 40
	for y := 0; y < h; y++ {
		for x := 0; x < samples; x++ {
			from := margin + x*(w-2*margin)/samples
			to := margin + (x+1)*(w-2*margin)/samples
			to = max(from+1, to)
			sum := 0
			for col := from; col < to; col++ {
				p := y*im.Stride + 4*col
				sum += 77*int(im.Pix[p]) + 150*int(im.Pix[p+1]) + 29*int(im.Pix[p+2])
			}
			gray[y*samples+x] = byte(sum / (256 * (to - from)))
		}
	}
	return frame{im, gray}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (f frame) fixedEdge(p frame, bottom bool) int {
	h := f.image.Bounds().Dy()
	for n := 0; n < h/3; n++ {
		row := n
		if bottom {
			row = h - n - 1
		}
		d := 0
		for x := 0; x < samples; x++ {
			i := row*samples + x
			d += abs(int(f.gray[i]) - int(p.gray[i]))
		}
		if d > samples {
			if n >= 8 {
				return n
			}
			break
		}
	}
	return 0
}

type candidate struct {
	offset int
	error  float64
}

func (f frame) displacement(p frame, top, bottom, maxOffset int, robust bool) int {
	end := f.image.Bounds().Dy() - bottom
	span := end - top
	if span < 64 || maxOffset < 0 {
		return -1
	}

	detail := make([]int, end)
	for y := top + 1; y < end; y++ {
		for x := 0; x < samples; x++ {
			i := y*samples + x
			detail[y] += abs(int(f.gray[i]) - int(f.gray[i-samples]))
		}
	}

	limit := min(maxOffset, span-max(80, span/4))
	var matches []candidate
	for shift := 0; shift <= limit; shift++ {
		overlap := span - shift
		var difference, texture [4]int
		step := max(1, overlap/28)
		for from := top + 1; from < end-shift; from += step {
			row := from
			for y := from + 1; y < min(from+step, end-shift); y++ {
				if detail[y] > detail[row] {
					row = y
				}
			}
			band := (row - top) * 4 / overlap
			texture[band] += detail[row]
			for x := 0; x < samples; x++ {
				i := row*samples + x
				difference[band] += abs(int(f.gray[i]) - int(p.gray[i+shift*samples]))
			}
		}
		d, t, good := 0, 0, 0
		for band := range difference {
			d += difference[band]
			t += texture[band]
			if texture[band] >= samples*3 && float64(difference[band]) < float64(texture[band])*0.55 {
				good++
			}
		}
		if robust && good < 2 || !robust && (t < samples*8 || float64(d) > 0.55*float64(t)) {
			continue
		}
		if score, ok := f.verify(p, top, end-shift, shift, robust); ok {
			matches = append(matches, candidate{shift, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].error < matches[j].error })
	if len(matches) == 0 {
		return -1
	}
	if len(matches) > 1 && matches[1].error <= matches[0].error+0.015 {
		return -1
	}
	return matches[0].offset
}

func (f frame) verify(p frame, top, end, shift int, robust bool) (float64, bool) {
	var scores []float64
	agreeingBands := 0
	totalDifference, totalTexture := 0, 0
	for band := 0; band < 4; band++ {
		agreeingTiles := 0
		for tile := 0; tile < 4; tile++ {
			difference, texture := 0, 0
			for y := max(1, top+(end-top)*band/4); y < top+(end-top)*(band+1)/4; y++ {
				for x := tile * samples / 4; x < (tile+1)*samples/4; x++ {
					i := y*samples + x
					v := int(f.gray[i])
					difference += abs(v - int(p.gray[i+shift*samples]))
					texture += abs(v - int(f.gray[i-samples]))
				}
			}
			totalDifference += difference
			totalTexture += texture
			if texture >= samples*2 {
				score := float64(difference) / float64(texture)
				scores = append(scores, score)
				if score < 0.22 {
					agreeingTiles++
				}
			}
		}
		if agreeingTiles >= 2 {
			agreeingBands++
		}
	}
	if !robust {
		if totalTexture < samples*8 {
			return 0, false
		}
		score := float64(totalDifference) / float64(totalTexture)
		return score, score < 0.22
	}
	if agreeingBands < 2 || len(scores) < 6 {
		return 0, false
	}
	sort.Float64s(scores)
	sum := 0.0
	for _, score := range scores[:(len(scores)+1)/2] {
		sum += score
	}
	score := sum / float64((len(scores)+1)/2)
	return score, score < 0.16
}

type appendResult int

const (
	added appendResult = iota
	unchanged
	mismatch
	full
)

type stitcher struct {
	previous frame
	strips   [][]byte
	ending   []byte
	width    int
	height   int
	seam     int
	footer   int
}

func (s *stitcher) scrolled() bool { return len(s.strips) > 1 }

func (s *stitcher) append(im *image.RGBA) appendResult {
	w, h := im.Bounds().Dx(), im.Bounds().Dy()
	if h > 65000 || w*h > 48_000_000 {
		return full
	}
	f := newFrame(im)
	if s.previous.image == nil {
		s.previous, s.width, s.height = f, w, h
		s.strips = [][]byte{im.Pix}
		return added
	}
	p := s.previous
	if p.image.Bounds() != im.Bounds() {
		return mismatch
	}
	if bytes.Equal(p.gray, f.gray) {
		return unchanged
	}
	top, bottom, seam := f.fixedEdge(p, false), s.footer, s.seam
	if !s.scrolled() {
		bottom = f.fixedEdge(p, true)
		seam = h - max(h/5, bottom+24)
	}
	offset := f.displacement(p, top, bottom, seam, false)
	if offset < 0 {
		offset = f.displacement(p, max(top, h/6), max(bottom, h/5), seam-max(top, h/6), true)
	}
	if offset < 0 {
		return mismatch
	}
	if offset == 0 {
		return unchanged
	}
	if s.height+offset > 65000 || w*(s.height+offset) > 48_000_000 {
		return full
	}
	if !s.scrolled() {
		s.strips[0] = bytes.Clone(p.image.Pix[:seam*p.image.Stride])
		s.seam, s.footer = seam, bottom
	}
	s.strips = append(s.strips, bytes.Clone(im.Pix[(seam-offset)*im.Stride:seam*im.Stride]))
	s.ending = bytes.Clone(im.Pix[seam*im.Stride:])
	s.height += offset
	s.previous = f
	return added
}

func (s *stitcher) image() *image.RGBA {
	result := image.NewRGBA(image.Rect(0, 0, s.width, s.height))
	p := 0
	for _, strip := range s.strips {
		p += copy(result.Pix[p:], strip)
	}
	copy(result.Pix[p:], s.ending)
	return result
}
