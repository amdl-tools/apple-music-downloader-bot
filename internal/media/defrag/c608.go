package fmp4unfrag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Apple Music videos carry closed captions as a QuickTime "c608" track.
// Each source sample holds a complete caption command sequence
// (RCL, text, EDM, EOC, ...) packed as many CEA-608 byte pairs, and its
// timestamp is the moment the FIRST pair is sent. The pairs are meant to be
// consumed one per video frame, so the caption is only displayed when the EOC
// command is reached, k pairs after the sample start.
//
// Players that hand the whole sample to their 608 decoder at once (ffmpeg,
// mpv, VLC, ...) execute every pair at the sample timestamp, so captions
// appear k/fps seconds early (0.7-1.7 s for typical lyric lines).
//
// spreadC608Samples rewrites each c608 track so that every sample holds exactly
// one pair and is timestamped when that pair is due. That is the canonical
// layout, so any player shows each caption at the intended time.

const (
	c608SampleSize = uint32(10) // size(4) + "cdat"(4) + one pair(2)
)

var c608NullPair = [2]byte{0x80, 0x80}

// layoutC608Pairs places every pair of every source sample on a one-pair-per-
// video-frame grid. durations are the source sample durations (ccTimescale
// ticks); the first sample starts at 0. Pairs that would collide with the
// previous sample's pairs queue behind them, as a paced decoder would.
//
// Times are computed in units of 1/(vTimescale*ccTimescale) seconds so the
// frame period (vFrame/vTimescale seconds) is exact for any frame rate.
//
// It returns the tick time of each output pair, the pairs, and the track end.
func layoutC608Pairs(
	source [][][2]byte,
	durations []uint32,
	ccTimescale, vTimescale, vFrame uint64,
) (times []uint64, pairs [][2]byte, end uint64) {
	period := vFrame * ccTimescale
	var cursor, start uint64

	for i, sp := range source {
		if len(sp) == 0 {
			// Empty source samples keep the timeline by emitting a null pair.
			sp = [][2]byte{c608NullPair}
		}

		u := start * vTimescale
		if cursor > u {
			u = cursor
		}

		for j, pair := range sp {
			t := (u + uint64(j)*period + vTimescale/2) / vTimescale
			times = append(times, t)
			pairs = append(pairs, pair)
		}

		cursor = u + uint64(len(sp))*period
		start += uint64(durations[i])
	}

	for k := 1; k < len(times); k++ {
		if times[k] <= times[k-1] {
			times[k] = times[k-1] + 1
		}
	}

	end = start
	if len(times) > 0 && times[len(times)-1]+1 > end {
		end = times[len(times)-1] + 1
	}
	return times, pairs, end
}

// parseC608Sample splits a QuickTime c608 sample into CEA-608 byte pairs.
// An all-zero sample is an empty sample. ok is false for any layout other than
// a sequence of "cdat" atoms (for example field-2 "cdt2" data), in which case
// the caller leaves the track untouched.
func parseC608Sample(b []byte) (pairs [][2]byte, ok bool) {
	allZero := true
	for _, v := range b {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, true
	}

	for len(b) > 0 {
		if len(b) < 8 {
			return nil, false
		}
		size := int(binary.BigEndian.Uint32(b[:4]))
		if size < 8 || size > len(b) || string(b[4:8]) != "cdat" || (size-8)%2 != 0 {
			return nil, false
		}
		for i := 8; i < size; i += 2 {
			pairs = append(pairs, [2]byte{b[i], b[i+1]})
		}
		b = b[size:]
	}
	return pairs, true
}

func isC608Track(trak *mp4.TrakBox) bool {
	if trak == nil || trak.Mdia == nil || trak.Mdia.Hdlr == nil ||
		trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil ||
		trak.Mdia.Minf.Stbl.Stsd == nil {
		return false
	}
	if trak.Mdia.Hdlr.HandlerType != "clcp" {
		return false
	}
	for _, child := range trak.Mdia.Minf.Stbl.Stsd.Children {
		if child != nil && child.Type() == "c608" {
			return true
		}
	}
	return false
}

// spreadC608Samples converts every c608 track to one pair per sample, paced at
// the video frame rate. It is a no-op when there is no c608 track or when a
// track uses an unexpected layout.
func spreadC608Samples(inputPath string, moov *mp4.MoovBox, tracks []*trackData) error {
	trakByID := make(map[uint32]*mp4.TrakBox, len(moov.Traks))
	for _, trak := range moov.Traks {
		trakByID[trak.Tkhd.TrackID] = trak
	}

	var (
		video     *trackData
		videoTrak *mp4.TrakBox
		captions  []*trackData
	)
	for _, td := range tracks {
		trak := trakByID[td.TrackID]
		if trak == nil || trak.Mdia == nil || trak.Mdia.Hdlr == nil {
			continue
		}
		switch {
		case trak.Mdia.Hdlr.HandlerType == "vide" && video == nil:
			video, videoTrak = td, trak
		case isC608Track(trak):
			captions = append(captions, td)
		}
	}
	if video == nil || len(captions) == 0 || len(video.Samples) == 0 {
		return nil
	}

	vTimescale := uint64(videoTrak.Mdia.Mdhd.Timescale)
	vFrame := uint64(video.Samples[0].Dur)
	if vTimescale == 0 || vFrame == 0 {
		return nil
	}

	in, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open source for closed captions: %w", err)
	}
	defer in.Close()

	for _, td := range captions {
		if err := spreadC608Track(in, trakByID[td.TrackID], td, vTimescale, vFrame); err != nil {
			return fmt.Errorf("track %d: %w", td.TrackID, err)
		}
	}
	return nil
}

func spreadC608Track(in *os.File, trak *mp4.TrakBox, td *trackData, vTimescale, vFrame uint64) error {
	ccTimescale := uint64(trak.Mdia.Mdhd.Timescale)
	if ccTimescale == 0 || len(td.Samples) == 0 || len(td.Chunks) == 0 {
		return nil
	}

	source := make([][][2]byte, 0, len(td.Samples))
	durations := make([]uint32, 0, len(td.Samples))
	desc := td.Chunks[0].SampleDesc
	sampleIndex := 0

	for _, chunk := range td.Chunks {
		if chunk.SampleDesc != desc || chunk.Source.Mdat == nil {
			return nil
		}

		var buf bytes.Buffer
		n, err := chunk.Source.Mdat.CopyData(
			int64(chunk.Source.Offset),
			int64(chunk.Source.Size),
			in,
			&buf,
		)
		if err != nil {
			return fmt.Errorf("read caption samples: %w", err)
		}
		if uint64(n) != chunk.Source.Size {
			return fmt.Errorf("read %d caption bytes, expected %d", n, chunk.Source.Size)
		}

		data := buf.Bytes()
		for i := uint32(0); i < chunk.SampleCount; i++ {
			if sampleIndex >= len(td.Samples) {
				return nil
			}
			sample := td.Samples[sampleIndex]
			if uint64(sample.Size) > uint64(len(data)) {
				return nil
			}
			pairs, ok := parseC608Sample(data[:sample.Size])
			if !ok {
				return nil
			}
			data = data[sample.Size:]
			source = append(source, pairs)
			durations = append(durations, sample.Dur)
			sampleIndex++
		}
	}
	if sampleIndex != len(td.Samples) {
		return nil
	}

	times, pairs, end := layoutC608Pairs(source, durations, ccTimescale, vTimescale, vFrame)
	if len(times) == 0 {
		return nil
	}

	samples := make([]mp4.Sample, len(times))
	payload := make([]byte, 0, len(times)*int(c608SampleSize))
	for k := range times {
		next := end
		if k+1 < len(times) {
			next = times[k+1]
		}
		dur := next - times[k]
		if dur > maxUint32 {
			return nil
		}

		// Flags 0 marks the sample as a sync sample.
		samples[k] = mp4.Sample{Dur: uint32(dur), Size: c608SampleSize}

		payload = binary.BigEndian.AppendUint32(payload, c608SampleSize)
		payload = append(payload, 'c', 'd', 'a', 't', pairs[k][0], pairs[k][1])
	}

	td.Samples = samples
	td.Chunks = []outputChunk{{
		Source:      sourceRange{Size: uint64(len(payload))},
		Data:        payload,
		SampleCount: uint32(len(samples)),
		SampleDesc:  desc,
	}}
	td.SampleGroups = nil
	td.Duration = end
	return nil
}
