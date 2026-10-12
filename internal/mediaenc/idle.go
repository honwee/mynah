package mediaenc

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"time"
)

// IdleLoop is a baked idle animation (VP8 in an IVF file, or H264 in the
// PLH264F1 framed container) that cored replays on the video track during
// silence — zero GPU and zero gRPC traffic per idle session. Loaded once at
// startup, shared read-only by all sessions. Frame 0 must be a keyframe (the
// bake script guarantees it).
//
// Two bake layouts are supported:
//   - legacy ping-pong: one segment, played linearly, wrapping to frame 0;
//   - multi-segment (anchors present): several motion segments, each starting
//     at an "anchor" keyframe whose pose is the exact neutral frame. All
//     anchors are pixel-identical, so the player may jump between segments
//     freely — randomized order kills the loop's visible periodicity, and
//     cutting back from speech at an anchor (neutral ≈ end-of-speech pose)
//     minimizes the seam.
type IdleLoop struct {
	Frames  [][]byte // complete encoded frames in play order
	Codec   string   // "vp8" or "h264"
	keys    []int    // indices of keyframes, ascending, keys[0] == 0
	anchors []int    // segment-start keyframes (neutral pose); empty = legacy
}

// LoadIdle parses a baked idle asset, sniffing the container from its magic:
// "DKIF" = IVF/VP8, "PLH264F1" = framed Annex-B H264, "PLH264F2" = same with
// anchor-flagged multi-segment layout.
func LoadIdle(path string) (*IdleLoop, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	switch {
	case len(data) >= 32 && string(data[0:4]) == "DKIF":
		return parseIVF(path, data)
	case len(data) >= 8 && string(data[0:8]) == "PLH264F1":
		return parseH264F(path, data)
	case len(data) >= 8 && string(data[0:8]) == "PLH264F2":
		return parseH264F2(path, data)
	default:
		return nil, fmt.Errorf("%s: unknown idle container (want IVF or PLH264F1/2)", path)
	}
}

// parseIVF: 32-byte file header, 12-byte frame headers; VP8 keyframes are
// detected from the frame tag (bit 0 of byte 0 is 0 for keyframes).
func parseIVF(path string, data []byte) (*IdleLoop, error) {
	l := &IdleLoop{Codec: "vp8"}
	off := 32
	for off+12 <= len(data) {
		sz := int(binary.LittleEndian.Uint32(data[off:]))
		off += 12
		if off+sz > len(data) {
			return nil, fmt.Errorf("%s: truncated frame at %d", path, off)
		}
		fr := data[off : off+sz]
		if len(fr) > 0 && fr[0]&0x01 == 0 {
			l.keys = append(l.keys, len(l.Frames))
		}
		l.Frames = append(l.Frames, fr)
		off += sz
	}
	return l.check(path)
}

// parseH264F: 8-byte magic, then per frame u32le size + u8 keyflag + data.
func parseH264F(path string, data []byte) (*IdleLoop, error) {
	l := &IdleLoop{Codec: "h264"}
	off := 8
	for off+5 <= len(data) {
		sz := int(binary.LittleEndian.Uint32(data[off:]))
		key := data[off+4] == 1
		off += 5
		if off+sz > len(data) {
			return nil, fmt.Errorf("%s: truncated frame at %d", path, off)
		}
		if key {
			l.keys = append(l.keys, len(l.Frames))
		}
		l.Frames = append(l.Frames, data[off:off+sz])
		off += sz
	}
	return l.check(path)
}

// parseH264F2: 8-byte magic, then per frame u32le size + u8 flag + data.
// flag: 0 = delta, 1 = keyframe, 2 = anchor keyframe (segment start, neutral
// pose). Produced by bake_idle.py --segments.
func parseH264F2(path string, data []byte) (*IdleLoop, error) {
	l := &IdleLoop{Codec: "h264"}
	off := 8
	for off+5 <= len(data) {
		sz := int(binary.LittleEndian.Uint32(data[off:]))
		flag := data[off+4]
		off += 5
		if off+sz > len(data) {
			return nil, fmt.Errorf("%s: truncated frame at %d", path, off)
		}
		if flag >= 1 {
			l.keys = append(l.keys, len(l.Frames))
		}
		if flag == 2 {
			l.anchors = append(l.anchors, len(l.Frames))
		}
		l.Frames = append(l.Frames, data[off:off+sz])
		off += sz
	}
	if len(l.anchors) < 2 {
		return nil, fmt.Errorf("%s: PLH264F2 needs >=2 anchor segments, got %d", path, len(l.anchors))
	}
	return l.check(path)
}

func (l *IdleLoop) check(path string) (*IdleLoop, error) {
	if len(l.Frames) == 0 {
		return nil, fmt.Errorf("%s: no frames", path)
	}
	if len(l.keys) == 0 || l.keys[0] != 0 {
		return nil, fmt.Errorf("%s: frame 0 is not a keyframe", path)
	}
	return l, nil
}

// NextKey returns the index of the first keyframe at or after pos (wrapping
// to 0). Re-entering the loop at a keyframe keeps the decoder's reference
// chain valid after it has been watching the speech stream.
func (l *IdleLoop) NextKey(pos int) int {
	for _, k := range l.keys {
		if k >= pos {
			return k
		}
	}
	return 0
}

// segEnd returns the exclusive end of the segment containing anchor index ai.
func (l *IdleLoop) segEnd(ai int) int {
	if ai+1 < len(l.anchors) {
		return l.anchors[ai+1]
	}
	return len(l.Frames)
}

// IdleCursor walks one session's position through the loop. Legacy assets play
// linearly with a wrap to 0; multi-segment assets hop to a random other
// segment at each segment end, so two loops through the library never repeat
// in the same order. Not safe for concurrent use (each VideoEncoder owns one).
type IdleCursor struct {
	l   *IdleLoop
	pos int
	seg int        // current anchor index (multi-segment only)
	rng *rand.Rand // per-session sequence; seeded from time
}

func (l *IdleLoop) NewCursor() *IdleCursor {
	c := &IdleCursor{l: l, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	if len(l.anchors) > 0 {
		c.seg = c.rng.Intn(len(l.anchors))
		c.pos = l.anchors[c.seg]
	}
	return c
}

// nextSeg picks a random segment other than the current one.
func (c *IdleCursor) nextSeg() int {
	n := len(c.l.anchors)
	s := c.rng.Intn(n - 1)
	if s >= c.seg {
		s++
	}
	return s
}

// Next returns the frame to play this tick and advances the cursor.
func (c *IdleCursor) Next() []byte {
	fr := c.l.Frames[c.pos]
	c.pos++
	if len(c.l.anchors) == 0 {
		if c.pos >= len(c.l.Frames) {
			c.pos = 0 // ping-pong bake; frame 0 is a keyframe
		}
		return fr
	}
	if c.pos >= c.l.segEnd(c.seg) {
		c.seg = c.nextSeg()
		c.pos = c.l.anchors[c.seg]
	}
	return fr
}

// Reenter repositions the cursor for cutting back from speech: the next frame
// must be a keyframe so the decoder's reference chain restarts. Multi-segment
// assets jump to a fresh random segment (every anchor is the neutral pose, so
// any of them is an equally small seam); legacy assets resume at the next
// keyframe past where the loop left off.
func (c *IdleCursor) Reenter() {
	if len(c.l.anchors) > 0 {
		c.seg = c.nextSeg()
		c.pos = c.l.anchors[c.seg]
		return
	}
	c.pos = c.l.NextKey(c.pos)
}
