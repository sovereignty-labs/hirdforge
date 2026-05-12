package main

import (
	"strings"
	"testing"
)

// feedAll pushes the chunks through the detector and returns the index of the
// chunk that tripped the detector, or -1 if it never did.
func feedAll(d *repetitionDetector, chunks []string) int {
	for i, c := range chunks {
		if d.observe(c) {
			return i
		}
	}
	return -1
}

func TestRepetitionDetector_QuietContent(t *testing.T) {
	d := newRepetitionDetector()
	// Feed ~1200 chars of monotonically varying tokens — the trailing 100-char
	// window will not appear earlier in the rolling buffer.
	for i := 0; i < 200; i++ {
		token := "token-" + strings.Repeat("x", i%7) + "-id-"
		// Make every observation unique by mixing in i.
		token += string(rune('a'+(i%26))) + string(rune('A'+(i%26))) + " "
		if d.observe(token) {
			t.Fatalf("varied content tripped detector at iteration %d (buf=%q)", i, string(d.buf))
		}
	}
}

func TestRepetitionDetector_CrossChunkBoundaryRepetition(t *testing.T) {
	// The scenario from the bug report: `call:task_status{task_id:abc}thought`
	// repeated many times but split across chunk boundaries that don't align
	// with the repeated token. The old chunk-equality check missed this.
	pattern := "call:task_status{task_id:abc}thought"
	stream := strings.Repeat(pattern, 28)

	// Slice the assembled stream into oddly-sized chunks so no two consecutive
	// chunks are byte-equal. 7 and 13 are coprime with len(pattern)=36.
	chunks := []string{}
	sizes := []int{7, 13, 5, 11, 9, 17}
	pos := 0
	for pos < len(stream) {
		sz := sizes[len(chunks)%len(sizes)]
		if pos+sz > len(stream) {
			sz = len(stream) - pos
		}
		chunks = append(chunks, stream[pos:pos+sz])
		pos += sz
	}

	d := newRepetitionDetector()
	tripped := feedAll(d, chunks)
	if tripped < 0 {
		t.Fatalf("detector failed to trip on %d chunks of cross-boundary repetition", len(chunks))
	}
}

func TestRepetitionDetector_NotEnoughOccurrencesIsQuiet(t *testing.T) {
	// Two copies of a 100-char window should not trip (threshold is 3).
	d := newRepetitionDetector()
	pattern := strings.Repeat("x", 100)
	// Feed 200 chars total (2 copies of the 100-char pattern). We feed in 50-
	// char chunks so every chunk triggers a check.
	for i := 0; i < 4; i++ {
		if d.observe(pattern[:50]) {
			t.Fatalf("detector tripped after only %d * 50 chars", i+1)
		}
	}
}

func TestRepetitionDetector_ThreeOccurrencesTrips(t *testing.T) {
	// Three back-to-back copies of an identical 100-char window should trip.
	d := newRepetitionDetector()
	pattern := strings.Repeat("y", 100)
	for i := 0; i < 5; i++ {
		// 50-char chunks so the check runs each call.
		_ = d.observe(pattern[:50])
		if d.observe(pattern[:50]) {
			return
		}
	}
	t.Fatalf("detector failed to trip on 3+ identical 100-char windows")
}

func TestRepetitionDetector_ChecksOnlyEveryInterval(t *testing.T) {
	// Small chunks below the interval should not run the check yet, even if a
	// repetition is technically present. This documents the batched-check
	// behavior so a future refactor doesn't accidentally regress it.
	d := newRepetitionDetector()
	// Pre-seed 350 chars of 'z'. The trailing 100-char window is all z's,
	// which appears 3 non-overlapping times in 350 z's.
	d.buf = append(d.buf, strings.Repeat("z", 350)...)
	// Feed 49 z's — under the interval, no check this turn.
	if d.observe(strings.Repeat("z", 49)) {
		t.Fatalf("detector ran check before reaching interval")
	}
	// One more z crosses the interval — should trip now.
	if !d.observe("z") {
		t.Fatalf("detector failed to trip on the interval boundary")
	}
}

func TestRepetitionDetector_EmptyContentIsNoop(t *testing.T) {
	d := newRepetitionDetector()
	if d.observe("") {
		t.Fatalf("empty content should never trip")
	}
	if len(d.buf) != 0 || d.sinceLastCheck != 0 {
		t.Fatalf("empty content mutated state: buf=%d sinceLastCheck=%d", len(d.buf), d.sinceLastCheck)
	}
}
