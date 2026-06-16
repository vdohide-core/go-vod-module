package hls

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"go-vod-module/internal/mp4"

	"github.com/asticode/go-astits"
)

type mergedSample struct {
	track     *mp4.Track
	sample    *mp4.Sample
	pts90k    int64
	dts90k    int64
	trackType string // "video" or "audio"
}

func MuxSegment(ctx context.Context, meta *mp4.MovieMetadata, segment HLSSegment, filePath string, writer io.Writer) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open source MP4: %w", err)
	}
	defer file.Close()

	// 1. Find tracks
	var videoTrack *mp4.Track
	var audioTrack *mp4.Track
	for _, track := range meta.Tracks {
		switch track.Type {
		case "video":
			videoTrack = track
		case "audio":
			audioTrack = track
		}
	}

	// 2. Prepare merged sample list sorted by DTS
	var samples []*mergedSample

	if videoTrack != nil && segment.StartSampleV < len(videoTrack.Samples) {
		endV := segment.EndSampleV
		if endV > len(videoTrack.Samples) {
			endV = len(videoTrack.Samples)
		}
		for i := segment.StartSampleV; i < endV; i++ {
			s := &videoTrack.Samples[i]
			samples = append(samples, &mergedSample{
				track:     videoTrack,
				sample:    s,
				pts90k:    s.PTS * 90000 / int64(videoTrack.Timescale),
				dts90k:    s.DTS * 90000 / int64(videoTrack.Timescale),
				trackType: "video",
			})
		}
	}

	if audioTrack != nil && segment.StartSampleA < len(audioTrack.Samples) {
		endA := segment.EndSampleA
		if endA > len(audioTrack.Samples) {
			endA = len(audioTrack.Samples)
		}
		for i := segment.StartSampleA; i < endA; i++ {
			s := &audioTrack.Samples[i]
			samples = append(samples, &mergedSample{
				track:     audioTrack,
				sample:    s,
				pts90k:    s.PTS * 90000 / int64(audioTrack.Timescale),
				dts90k:    s.DTS * 90000 / int64(audioTrack.Timescale),
				trackType: "audio",
			})
		}
	}

	// Sort samples by DTS (decoding timestamp order) for correct multiplexing
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].dts90k < samples[j].dts90k
	})

	// 3. Initialize Muxer
	mx := astits.NewMuxer(ctx, writer)

	if videoTrack != nil {
		mx.AddElementaryStream(astits.PMTElementaryStream{
			ElementaryPID: 256,
			StreamType:    astits.StreamTypeH264Video,
		})
	}

	if audioTrack != nil {
		mx.AddElementaryStream(astits.PMTElementaryStream{
			ElementaryPID: 257,
			StreamType:    astits.StreamTypeAACAudio,
		})
	}

	// Set PCR PID to video stream (256) or audio stream (257)
	if videoTrack != nil {
		mx.SetPCRPID(256)
	} else if audioTrack != nil {
		mx.SetPCRPID(257)
	}

	// Write PAT and PMT tables
	if _, err := mx.WriteTables(); err != nil {
		return fmt.Errorf("failed to write initial TS tables: %w", err)
	}

	// 4. Parse codec metadata for configuration NALs
	var spsList, ppsList [][]byte
	if videoTrack != nil && len(videoTrack.CodecBox) > 0 {
		spsList, ppsList, _ = extractSPSandPPS(videoTrack.CodecBox)
	}

	var audioProfile, audioFreqIdx, audioChanCfg byte
	if audioTrack != nil && len(audioTrack.CodecBox) > 0 {
		audioProfile, audioFreqIdx, audioChanCfg, _ = parseAudioSpecificConfig(audioTrack.CodecBox)
	} else {
		audioProfile, audioFreqIdx, audioChanCfg = 1, 4, 2 // Defaults (AAC-LC, 44.1kHz, Stereo)
	}

	// 5. Mux all samples in sorted order
	for _, s := range samples {
		rawBytes := make([]byte, s.sample.Size)
		if _, err := file.ReadAt(rawBytes, s.sample.Offset); err != nil {
			return fmt.Errorf("failed to read sample: %w", err)
		}

		switch s.trackType {
		case "video":
			// Convert H.264 AVCC to Annex B
			// SPS/PPS must be prepended at every keyframe (IDR) so the decoder
			// can initialize correctly when starting playback from any segment
			writeExtra := s.sample.IsKeyframe
			annexBBytes, err := avccToAnnexB(rawBytes, spsList, ppsList, writeExtra)
			if err != nil {
				return fmt.Errorf("failed to convert video to Annex B: %w", err)
			}

			_, err = mx.WriteData(&astits.MuxerData{
				PID: 256,
				PES: &astits.PESData{
					Header: &astits.PESHeader{
						StreamID: 0xE0, // Video Stream 1 ID
						OptionalHeader: &astits.PESOptionalHeader{
							MarkerBits:        2,
							PTSDTSIndicator:   3, // Both PTS and DTS
							PTS:               &astits.ClockReference{Base: s.pts90k},
							DTS:               &astits.ClockReference{Base: s.dts90k},
							HasOptionalFields: true,
						},
					},
					Data: annexBBytes,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to write video TS data: %w", err)
			}
		case "audio":
			// Generate ADTS header and prepend to AAC sample
			adtsHeader := getADTSHeader(audioProfile, audioFreqIdx, audioChanCfg, uint32(len(rawBytes)))
			audioPayload := append(adtsHeader, rawBytes...)

			_, err = mx.WriteData(&astits.MuxerData{
				PID: 257,
				PES: &astits.PESData{
					Header: &astits.PESHeader{
						StreamID: 0xC0, // Audio Stream 1 ID
						OptionalHeader: &astits.PESOptionalHeader{
							MarkerBits:        2,
							PTSDTSIndicator:   2, // PTS only
							PTS:               &astits.ClockReference{Base: s.pts90k},
							HasOptionalFields: true,
						},
					},
					Data: audioPayload,
				},
			})
			if err != nil {
				return fmt.Errorf("failed to write audio TS data: %w", err)
			}
		}
	}

	return nil
}

func extractSPSandPPS(codecBox []byte) (spsList [][]byte, ppsList [][]byte, err error) {
	if len(codecBox) < 14 {
		return nil, nil, fmt.Errorf("codec box too small")
	}
	payload := codecBox[8:]
	if len(payload) < 6 {
		return nil, nil, fmt.Errorf("avcC payload too small")
	}

	numSPS := int(payload[5] & 0x1F)
	idx := 6
	for i := 0; i < numSPS; i++ {
		if idx+2 > len(payload) {
			return nil, nil, fmt.Errorf("truncated SPS length")
		}
		spsLen := int(uint16(payload[idx])<<8 | uint16(payload[idx+1]))
		idx += 2
		if idx+spsLen > len(payload) {
			return nil, nil, fmt.Errorf("truncated SPS data")
		}
		sps := make([]byte, spsLen)
		copy(sps, payload[idx:idx+spsLen])
		spsList = append(spsList, sps)
		idx += spsLen
	}

	if idx+1 > len(payload) {
		return nil, nil, fmt.Errorf("truncated PPS count")
	}
	numPPS := int(payload[idx])
	idx++
	for i := 0; i < numPPS; i++ {
		if idx+2 > len(payload) {
			return nil, nil, fmt.Errorf("truncated PPS length")
		}
		ppsLen := int(uint16(payload[idx])<<8 | uint16(payload[idx+1]))
		idx += 2
		if idx+ppsLen > len(payload) {
			return nil, nil, fmt.Errorf("truncated PPS data")
		}
		pps := make([]byte, ppsLen)
		copy(pps, payload[idx:idx+ppsLen])
		ppsList = append(ppsList, pps)
		idx += ppsLen
	}

	return spsList, ppsList, nil
}

func parseAudioSpecificConfig(codecBox []byte) (profile byte, freqIdx byte, chanCfg byte, err error) {
	if len(codecBox) < 12 {
		return 1, 4, 2, fmt.Errorf("codec box too small")
	}
	payload := codecBox[8:]
	var asc []byte
	for i := 0; i < len(payload)-2; i++ {
		if payload[i] == 5 { // Tag 5 (DecSpecificInfoTag)
			size := int(payload[i+1])
			if i+2+size <= len(payload) {
				asc = payload[i+2 : i+2+size]
				break
			}
		}
	}
	if len(asc) < 2 {
		return 1, 4, 2, nil // default fallbacks
	}

	objType := asc[0] >> 3
	profile = objType - 1 // 1 for AAC-LC
	freqIdx = ((asc[0] & 7) << 1) | (asc[1] >> 7)
	chanCfg = (asc[1] >> 3) & 15

	return profile, freqIdx, chanCfg, nil
}

// Convert AVCC size-prefixed NAL units into Annex B start-code format
func avccToAnnexB(avcc []byte, spsList [][]byte, ppsList [][]byte, writeExtra bool) ([]byte, error) {
	var out []byte

	// 1. Prepend Access Unit Delimiter (AUD) NAL unit
	out = append(out, []byte{0x00, 0x00, 0x00, 0x01, 0x09, 0xf0}...)

	// 2. Prepend SPS/PPS configuration NALs on first video frame or keyframes
	if writeExtra {
		for _, sps := range spsList {
			out = append(out, []byte{0x00, 0x00, 0x00, 0x01}...)
			out = append(out, sps...)
		}
		for _, pps := range ppsList {
			out = append(out, []byte{0x00, 0x00, 0x00, 0x01}...)
			out = append(out, pps...)
		}
	}

	// 3. Process NALs in sample
	idx := 0
	for idx < len(avcc) {
		if idx+4 > len(avcc) {
			break
		}
		// Read 4-byte big-endian size
		nalSize := uint32(avcc[idx])<<24 | uint32(avcc[idx+1])<<16 | uint32(avcc[idx+2])<<8 | uint32(avcc[idx+3])
		idx += 4

		if idx+int(nalSize) > len(avcc) {
			return nil, fmt.Errorf("truncated NAL unit data")
		}

		// Write start code and NAL unit content
		out = append(out, []byte{0x00, 0x00, 0x00, 0x01}...)
		out = append(out, avcc[idx:idx+int(nalSize)]...)
		idx += int(nalSize)
	}

	return out, nil
}

// Generate 7-byte ADTS header
func getADTSHeader(profile byte, freqIdx byte, chanCfg byte, frameLen uint32) []byte {
	header := make([]byte, 7)
	// Syncword 12 bits: 0xFFF
	header[0] = 0xFF
	header[1] = 0xF0
	// Layer (2 bits): 00
	// Protection absent (1 bit): 1
	header[1] |= 0x01

	header[2] = (profile << 6) | (freqIdx << 2) | (chanCfg >> 2)
	header[3] = ((chanCfg & 3) << 6)

	// total length = ADTS header (7) + raw AAC frame length
	totalLen := frameLen + 7
	header[3] |= byte((totalLen >> 11) & 3)
	header[4] = byte(totalLen >> 3)
	header[5] = byte((totalLen & 7) << 5)

	// Buffer fullness (11 bits): 0x7FF
	header[5] |= 0x1F
	header[6] = 0xFC // Number of raw data blocks: 0 (2 bits) -> 0xFC

	return header
}
