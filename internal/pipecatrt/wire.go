// Package pipecatrt speaks Pipecat's WebSocket transport wire format so cored
// can use a Pipecat pipeline as a conversation brain ("brain mode pipecat"):
// cored streams the visitor's microphone audio to a Pipecat bot (which runs
// VAD -> STT -> LLM -> TTS with whatever services it likes) and plays the
// bot's TTS audio through the avatar engine, so the face lip-syncs whatever
// the Pipecat pipeline says.
//
// Wire format = Pipecat's ProtobufFrameSerializer (pipecat/frames/protobufs/
// frames.proto): one binary WebSocket message per frame,
//
//	Frame { oneof frame { TextFrame text=1; AudioRawFrame audio=2;
//	        TranscriptionFrame transcription=3; MessageFrame message=4;
//	        InterruptionFrame interruption=5 } }
//
// encoded here by hand with protowire (five tiny messages; no generated code
// to keep in sync with Pipecat's repo).
package pipecatrt

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// Frame kinds on the wire.
const (
	KindText          = 1
	KindAudio         = 2
	KindTranscription = 3
	KindMessage       = 4
	KindInterruption  = 5
)

// Audio is Pipecat's AudioRawFrame: raw 16-bit little-endian PCM.
type Audio struct {
	ID          uint64
	Name        string
	PCM         []byte
	SampleRate  uint32
	NumChannels uint32
	PTS         uint64
}

// Text is TextFrame / TranscriptionFrame (both carry text; transcription adds
// user_id + timestamp which we ignore on receive and leave empty on send).
type Text struct {
	ID   uint64
	Name string
	Text string
}

// Decoded is one received frame; exactly one field is set per Kind.
type Decoded struct {
	Kind      int
	Audio     *Audio
	Text      *Text  // KindText, KindTranscription
	Data      string // KindMessage (JSON)
	Interrupt bool   // KindInterruption
}

func appendText(b []byte, id uint64, name, text string) []byte {
	if id != 0 {
		b = protowire.AppendTag(b, 1, protowire.VarintType)
		b = protowire.AppendVarint(b, id)
	}
	if name != "" {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendString(b, name)
	}
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendString(b, text)
	return b
}

// EncodeAudio wraps PCM as Frame{audio: AudioRawFrame{...}}.
func EncodeAudio(a Audio) []byte {
	var inner []byte
	if a.ID != 0 {
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, a.ID)
	}
	if a.Name != "" {
		inner = protowire.AppendTag(inner, 2, protowire.BytesType)
		inner = protowire.AppendString(inner, a.Name)
	}
	inner = protowire.AppendTag(inner, 3, protowire.BytesType)
	inner = protowire.AppendBytes(inner, a.PCM)
	inner = protowire.AppendTag(inner, 4, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(a.SampleRate))
	inner = protowire.AppendTag(inner, 5, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(a.NumChannels))
	if a.PTS != 0 {
		inner = protowire.AppendTag(inner, 6, protowire.VarintType)
		inner = protowire.AppendVarint(inner, a.PTS)
	}
	b := protowire.AppendTag(nil, KindAudio, protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

// EncodeText wraps a TextFrame (kind=KindText) or TranscriptionFrame
// (kind=KindTranscription).
func EncodeText(kind int, t Text) []byte {
	inner := appendText(nil, t.ID, t.Name, t.Text)
	b := protowire.AppendTag(nil, protowire.Number(kind), protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

// EncodeInterruption wraps an InterruptionFrame.
func EncodeInterruption(id uint64) []byte {
	var inner []byte
	if id != 0 {
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, id)
	}
	b := protowire.AppendTag(nil, KindInterruption, protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

// Decode parses one Frame message.
func Decode(msg []byte) (*Decoded, error) {
	num, typ, n := protowire.ConsumeTag(msg)
	if n < 0 {
		return nil, errors.New("pipecat frame: bad tag")
	}
	if typ != protowire.BytesType {
		return nil, fmt.Errorf("pipecat frame: field %d is not a message", num)
	}
	inner, m := protowire.ConsumeBytes(msg[n:])
	if m < 0 {
		return nil, errors.New("pipecat frame: bad length")
	}
	d := &Decoded{Kind: int(num)}
	switch num {
	case KindAudio:
		a := &Audio{}
		if err := walk(inner, func(f protowire.Number, t protowire.Type, v []byte, u uint64) {
			switch f {
			case 1:
				a.ID = u
			case 2:
				a.Name = string(v)
			case 3:
				a.PCM = append([]byte(nil), v...)
			case 4:
				a.SampleRate = uint32(u)
			case 5:
				a.NumChannels = uint32(u)
			case 6:
				a.PTS = u
			}
		}); err != nil {
			return nil, err
		}
		d.Audio = a
	case KindText, KindTranscription:
		t := &Text{}
		if err := walk(inner, func(f protowire.Number, _ protowire.Type, v []byte, u uint64) {
			switch f {
			case 1:
				t.ID = u
			case 2:
				t.Name = string(v)
			case 3:
				t.Text = string(v)
			}
		}); err != nil {
			return nil, err
		}
		d.Text = t
	case KindMessage:
		if err := walk(inner, func(f protowire.Number, _ protowire.Type, v []byte, _ uint64) {
			if f == 1 {
				d.Data = string(v)
			}
		}); err != nil {
			return nil, err
		}
	case KindInterruption:
		d.Interrupt = true
	default:
		return nil, fmt.Errorf("pipecat frame: unknown field %d", num)
	}
	return d, nil
}

// walk iterates fields of a message, handing bytes fields as v and varints as u.
func walk(b []byte, fn func(f protowire.Number, t protowire.Type, v []byte, u uint64)) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return errors.New("pipecat frame: bad inner tag")
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			u, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return errors.New("pipecat frame: bad varint")
			}
			fn(num, typ, nil, u)
			b = b[m:]
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return errors.New("pipecat frame: bad bytes")
			}
			fn(num, typ, v, 0)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return errors.New("pipecat frame: bad field")
			}
			b = b[m:]
		}
	}
	return nil
}
