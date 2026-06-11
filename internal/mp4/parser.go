package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"
)

type Sample struct {
	Offset     int64
	Size       uint32
	PTS        int64 // Presentation timestamp (in timescale units)
	DTS        int64 // Decoding timestamp (in timescale units)
	IsKeyframe bool
}

type Track struct {
	ID        uint32
	Type      string // "video" or "audio"
	Codec     string // "avc1", "mp4a", etc.
	Timescale uint32
	Duration  int64  // in timescale units
	Width     uint16 // Video only
	Height    uint16 // Video only
	CodecBox  []byte // Raw avcC or esds box bytes (header + payload)
	Samples   []Sample
}

type MovieMetadata struct {
	Duration  int64 // in movie timescale units
	Timescale uint32
	Tracks    []*Track
}

type SttsEntry struct {
	SampleCount uint32
	SampleDelta uint32
}

type CttsEntry struct {
	SampleCount  uint32
	SampleOffset int32
}

type StscEntry struct {
	FirstChunk             uint32
	SamplesPerChunk        uint32
	SampleDescriptionIndex uint32
}

type rawTrackInfo struct {
	id        uint32
	handler   string // "vide" or "soun"
	timescale uint32
	duration  int64
	width     uint16
	height    uint16
	codec     string
	codecBox  []byte

	// Sample tables parsed efficiently
	stts []SttsEntry
	ctts []CttsEntry
	stsz struct {
		SampleSize  uint32
		SampleCount uint32
		EntrySize   []uint32
	}
	stsc []StscEntry
	stco []uint32
	co64 []uint64
	stss []uint32
}

func Parse(filePath string) (*MovieMetadata, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	meta := &MovieMetadata{
		Tracks: make([]*Track, 0),
	}

	var tracks []*rawTrackInfo
	var currentTrack *rawTrackInfo

	containers := map[string]bool{
		"moov": true,
		"trak": true,
		"mdia": true,
		"minf": true,
		"stbl": true,
		"stsd": true,
		"avc1": true,
		"mp4a": true,
	}

	_, err = mp4.ReadBoxStructure(file, func(h *mp4.ReadHandle) (interface{}, error) {
		if !h.BoxInfo.IsSupportedType() {
			return nil, nil
		}
		boxType := h.BoxInfo.Type.String()

		switch boxType {
		case "mvhd":
			payload, _, err := h.ReadPayload()
			if err == nil {
				mvhd := payload.(*mp4.Mvhd)
				if mvhd.Version == 1 {
					meta.Timescale = mvhd.Timescale
					meta.Duration = int64(mvhd.DurationV1)
				} else {
					meta.Timescale = mvhd.Timescale
					meta.Duration = int64(mvhd.DurationV0)
				}
			}

		case "trak":
			currentTrack = &rawTrackInfo{}
			tracks = append(tracks, currentTrack)

		case "tkhd":
			if currentTrack != nil {
				payload, _, err := h.ReadPayload()
				if err == nil {
					tkhd := payload.(*mp4.Tkhd)
					currentTrack.id = tkhd.TrackID
					// Width and height are 16.16 fixed point
					currentTrack.width = uint16(tkhd.Width >> 16)
					currentTrack.height = uint16(tkhd.Height >> 16)
				}
			}

		case "mdhd":
			if currentTrack != nil {
				payload, _, err := h.ReadPayload()
				if err == nil {
					mdhd := payload.(*mp4.Mdhd)
					currentTrack.timescale = mdhd.Timescale
					if mdhd.Version == 1 {
						currentTrack.duration = int64(mdhd.DurationV1)
					} else {
						currentTrack.duration = int64(mdhd.DurationV0)
					}
				}
			}

		case "hdlr":
			if currentTrack != nil && len(h.Path) >= 3 && h.Path[len(h.Path)-2].String() == "mdia" {
				payload, _, err := h.ReadPayload()
				if err == nil {
					hdlr := payload.(*mp4.Hdlr)
					handlerType := string(hdlr.HandlerType[:])
					currentTrack.handler = handlerType
				}
			}

		case "avc1":
			if currentTrack != nil {
				currentTrack.codec = "avc1"
			}

		case "mp4a":
			if currentTrack != nil {
				currentTrack.codec = "mp4a"
			}

		case "avcC", "esds":
			if currentTrack != nil {
				// Read raw box bytes (header + payload)
				boxBytes := make([]byte, h.BoxInfo.Size)
				_, err := file.ReadAt(boxBytes, int64(h.BoxInfo.Offset))
				if err == nil {
					currentTrack.codecBox = boxBytes
				}
			}

		case "stts":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					entries := make([]SttsEntry, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*8+8 <= uint32(len(payload)) {
							entries[i] = SttsEntry{
								SampleCount: binary.BigEndian.Uint32(payload[8+i*8 : 12+i*8]),
								SampleDelta: binary.BigEndian.Uint32(payload[12+i*8 : 16+i*8]),
							}
						}
					}
					currentTrack.stts = entries
				}
			}

		case "ctts":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					entries := make([]CttsEntry, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*8+8 <= uint32(len(payload)) {
							offset := int32(binary.BigEndian.Uint32(payload[12+i*8 : 16+i*8]))
							entries[i] = CttsEntry{
								SampleCount:  binary.BigEndian.Uint32(payload[8+i*8 : 12+i*8]),
								SampleOffset: offset,
							}
						}
					}
					currentTrack.ctts = entries
				}
			}

		case "stsz":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 12 {
					uniformSize := binary.BigEndian.Uint32(payload[4:8])
					count := binary.BigEndian.Uint32(payload[8:12])
					currentTrack.stsz.SampleSize = uniformSize
					currentTrack.stsz.SampleCount = count

					if uniformSize == 0 {
						sizes := make([]uint32, count)
						for i := uint32(0); i < count; i++ {
							if 12+i*4+4 <= uint32(len(payload)) {
								sizes[i] = binary.BigEndian.Uint32(payload[12+i*4 : 16+i*4])
							}
						}
						currentTrack.stsz.EntrySize = sizes
					}
				}
			}

		case "stsc":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					entries := make([]StscEntry, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*12+12 <= uint32(len(payload)) {
							entries[i] = StscEntry{
								FirstChunk:             binary.BigEndian.Uint32(payload[8+i*12 : 12+i*12]),
								SamplesPerChunk:        binary.BigEndian.Uint32(payload[12+i*12 : 16+i*12]),
								SampleDescriptionIndex: binary.BigEndian.Uint32(payload[16+i*12 : 20+i*12]),
							}
						}
					}
					currentTrack.stsc = entries
				}
			}

		case "stco":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					offsets := make([]uint32, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*4+4 <= uint32(len(payload)) {
							offsets[i] = binary.BigEndian.Uint32(payload[8+i*4 : 12+i*4])
						}
					}
					currentTrack.stco = offsets
				}
			}

		case "co64":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					offsets := make([]uint64, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*8+8 <= uint32(len(payload)) {
							offsets[i] = binary.BigEndian.Uint64(payload[8+i*8 : 16+i*8])
						}
					}
					currentTrack.co64 = offsets
				}
			}

		case "stss":
			if currentTrack != nil {
				paySize := h.BoxInfo.Size - h.BoxInfo.HeaderSize
				payload := make([]byte, paySize)
				_, err := file.ReadAt(payload, int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize))
				if err == nil && len(payload) >= 8 {
					count := binary.BigEndian.Uint32(payload[4:8])
					sampleNumbers := make([]uint32, count)
					for i := uint32(0); i < count; i++ {
						if 8+i*4+4 <= uint32(len(payload)) {
							sampleNumbers[i] = binary.BigEndian.Uint32(payload[8+i*4 : 12+i*4])
						}
					}
					currentTrack.stss = sampleNumbers
				}
			}
		}

		if containers[boxType] {
			return h.Expand()
		}
		return nil, nil
	})

	// Some boxes might fail at the end (e.g. metadata or EOF details), we ignore it if we parsed tracks
	if err != nil && err != io.EOF && len(tracks) == 0 {
		return nil, fmt.Errorf("failed to parse box structure: %w", err)
	}

	// 5. Build tracks and samples
	for _, raw := range tracks {
		// Ignore tracks with missing essential info
		if raw.id == 0 || raw.handler == "" {
			continue
		}

		samples, err := raw.buildSamples()
		if err != nil {
			if raw.handler == "vide" || raw.handler == "soun" {
				return nil, fmt.Errorf("failed to build samples for track %d (%s): %w", raw.id, raw.handler, err)
			}
			continue
		}

		var trackType string
		switch raw.handler {
		case "vide":
			trackType = "video"
		case "soun":
			trackType = "audio"
		default:
			continue // skip subtitle or metadata tracks
		}

		meta.Tracks = append(meta.Tracks, &Track{
			ID:        raw.id,
			Type:      trackType,
			Codec:     raw.codec,
			Timescale: raw.timescale,
			Duration:  raw.duration,
			Width:     raw.width,
			Height:    raw.height,
			CodecBox:  raw.codecBox,
			Samples:   samples,
		})
	}

	return meta, nil
}

func (t *rawTrackInfo) buildSamples() ([]Sample, error) {
	if t.stsz.SampleCount == 0 || len(t.stsc) == 0 || (len(t.stco) == 0 && len(t.co64) == 0) || len(t.stts) == 0 {
		return nil, fmt.Errorf("missing critical sample tables")
	}

	// 1. Get sample sizes
	numSamples := t.stsz.SampleCount
	sampleSizes := make([]uint32, numSamples)
	if t.stsz.SampleSize > 0 {
		for i := uint32(0); i < numSamples; i++ {
			sampleSizes[i] = t.stsz.SampleSize
		}
	} else {
		if len(t.stsz.EntrySize) < int(numSamples) {
			return nil, fmt.Errorf("stsz entry count mismatch: expected %d, got %d", numSamples, len(t.stsz.EntrySize))
		}
		for i := uint32(0); i < numSamples; i++ {
			sampleSizes[i] = t.stsz.EntrySize[i]
		}
	}

	// 2. Get chunk offsets
	var chunkOffsets []int64
	if len(t.stco) > 0 {
		chunkOffsets = make([]int64, len(t.stco))
		for i, offset := range t.stco {
			chunkOffsets[i] = int64(offset)
		}
	} else if len(t.co64) > 0 {
		chunkOffsets = make([]int64, len(t.co64))
		for i, offset := range t.co64 {
			chunkOffsets[i] = int64(offset)
		}
	}

	// 3. Map samples to chunks and compute offsets
	sampleOffsets := make([]int64, numSamples)
	stscEntries := t.stsc
	if len(stscEntries) == 0 {
		return nil, fmt.Errorf("stsc table is empty")
	}

	sampleIdx := uint32(0)
	currentChunk := uint32(1)

	for entryIdx := 0; entryIdx < len(stscEntries); entryIdx++ {
		firstChunk := stscEntries[entryIdx].FirstChunk
		samplesPerChunk := stscEntries[entryIdx].SamplesPerChunk

		nextFirstChunk := uint32(len(chunkOffsets) + 1)
		if entryIdx+1 < len(stscEntries) {
			nextFirstChunk = stscEntries[entryIdx+1].FirstChunk
		}

		for chunk := firstChunk; chunk < nextFirstChunk && chunk <= uint32(len(chunkOffsets)); chunk++ {
			chunkOffset := chunkOffsets[chunk-1]
			for s := uint32(0); s < samplesPerChunk; s++ {
				if sampleIdx >= numSamples {
					break
				}
				sampleOffsets[sampleIdx] = chunkOffset
				chunkOffset += int64(sampleSizes[sampleIdx])
				sampleIdx++
			}
			currentChunk++
		}
	}

	// 4. Compute Decoding Timestamps (DTS) using stts
	dts := make([]int64, numSamples)
	currentDTS := int64(0)
	sttsIdx := uint32(0)
	sttsEntries := t.stts

	for _, entry := range sttsEntries {
		for i := uint32(0); i < entry.SampleCount; i++ {
			if sttsIdx >= numSamples {
				break
			}
			dts[sttsIdx] = currentDTS
			currentDTS += int64(entry.SampleDelta)
			sttsIdx++
		}
	}

	// 5. Compute Presentation Timestamps (PTS) using ctts
	pts := make([]int64, numSamples)
	copy(pts, dts)
	if len(t.ctts) > 0 {
		cttsIdx := uint32(0)
		for _, entry := range t.ctts {
			offset := int64(entry.SampleOffset)
			for i := uint32(0); i < entry.SampleCount; i++ {
				if cttsIdx >= numSamples {
					break
				}
				pts[cttsIdx] = dts[cttsIdx] + offset
				cttsIdx++
			}
		}
	}

	// 6. Identify Keyframes using stss
	isKeyframe := make([]bool, numSamples)
	if len(t.stss) > 0 {
		for _, sampleNum := range t.stss {
			if sampleNum > 0 && sampleNum <= numSamples {
				isKeyframe[sampleNum-1] = true
			}
		}
	} else {
		for i := uint32(0); i < numSamples; i++ {
			isKeyframe[i] = true
		}
	}

	// 7. Package everything into Sample structs
	samples := make([]Sample, numSamples)
	for i := uint32(0); i < numSamples; i++ {
		samples[i] = Sample{
			Offset:     sampleOffsets[i],
			Size:       sampleSizes[i],
			PTS:        pts[i],
			DTS:        dts[i],
			IsKeyframe: isKeyframe[i],
		}
	}

	return samples, nil
}
