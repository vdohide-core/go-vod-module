package dash

import (
	"context"
	"fmt"
	"io"
	"os"

	"go-vod-module/internal/hls"
	mp4_parser "go-vod-module/internal/mp4"

	"github.com/abema/go-mp4"
)

type memoryWriteSeeker struct {
	buf []byte
	pos int
}

func (m *memoryWriteSeeker) Write(p []byte) (n int, err error) {
	end := m.pos + len(p)
	if end > len(m.buf) {
		newBuf := make([]byte, end)
		copy(newBuf, m.buf)
		m.buf = newBuf
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memoryWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = int(offset)
	case io.SeekCurrent:
		m.pos += int(offset)
	case io.SeekEnd:
		m.pos = len(m.buf) + int(offset)
	}
	return int64(m.pos), nil
}

type customAVC1 struct {
	mp4.VisualSampleEntry
}

func (c *customAVC1) GetType() mp4.BoxType {
	return mp4.BoxType{'a', 'v', 'c', '1'}
}

type customMP4A struct {
	mp4.AudioSampleEntry
}

func (c *customMP4A) GetType() mp4.BoxType {
	return mp4.BoxType{'m', 'p', '4', 'a'}
}

func MuxInitSegment(meta *mp4_parser.MovieMetadata, trackType string, writer io.Writer) error {
	var targetTrack *mp4_parser.Track
	for _, track := range meta.Tracks {
		if track.Type == trackType {
			targetTrack = track
			break
		}
	}
	if targetTrack == nil {
		return fmt.Errorf("track type %s not found in metadata", trackType)
	}

	mws := &memoryWriteSeeker{}
	w := mp4.NewWriter(mws)

	// 1. Write ftyp
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'f', 't', 'y', 'p'}})
	mp4.Marshal(w, ftyp, mp4.Context{})
	w.EndBox()

	// 2. Start moov
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'o', 'o', 'v'}})

	// mvhd
	mvhd := &mp4.Mvhd{
		Timescale:  1000,
		DurationV0: 0,
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'v', 'h', 'd'}})
	mp4.Marshal(w, mvhd, mp4.Context{})
	w.EndBox()

	// start trak
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'r', 'a', 'k'}})

	// tkhd
	tkhd := &mp4.Tkhd{
		TrackID:    targetTrack.ID,
		DurationV0: 0,
		Width:      uint32(targetTrack.Width) << 16,
		Height:     uint32(targetTrack.Height) << 16,
	}
	tkhd.Flags[2] = 3 // enabled + in_movie
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'k', 'h', 'd'}})
	mp4.Marshal(w, tkhd, mp4.Context{})
	w.EndBox()

	// start mdia
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'd', 'i', 'a'}})

	// mdhd
	mdhd := &mp4.Mdhd{
		Timescale:  targetTrack.Timescale,
		DurationV0: 0,
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'd', 'h', 'd'}})
	mp4.Marshal(w, mdhd, mp4.Context{})
	w.EndBox()

	// hdlr
	hdlr := &mp4.Hdlr{}
	if trackType == "video" {
		hdlr.HandlerType = [4]byte{'v', 'i', 'd', 'e'}
		hdlr.Name = "Video"
	} else {
		hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
		hdlr.Name = "Audio"
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'h', 'd', 'l', 'r'}})
	mp4.Marshal(w, hdlr, mp4.Context{})
	w.EndBox()

	// start minf
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'i', 'n', 'f'}})

	if trackType == "video" {
		vmhd := &mp4.Vmhd{}
		vmhd.Flags[2] = 1
		w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'v', 'm', 'h', 'd'}})
		mp4.Marshal(w, vmhd, mp4.Context{})
		w.EndBox()
	} else {
		smhd := &mp4.Smhd{}
		w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 'm', 'h', 'd'}})
		mp4.Marshal(w, smhd, mp4.Context{})
		w.EndBox()
	}

	// dinf -> dref
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'d', 'i', 'n', 'f'}})
	dref := &mp4.Dref{
		EntryCount: 1,
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'d', 'r', 'e', 'f'}})
	mp4.Marshal(w, dref, mp4.Context{})
	url := &mp4.Url{}
	url.Flags[2] = 1 // self-contained
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'u', 'r', 'l', ' '}})
	mp4.Marshal(w, url, mp4.Context{})
	w.EndBox() // url
	w.EndBox() // dref
	w.EndBox() // dinf

	// start stbl
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 'b', 'l'}})

	// stsd
	stsd := &mp4.Stsd{
		EntryCount: 1,
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 's', 'd'}})
	mp4.Marshal(w, stsd, mp4.Context{})

	if trackType == "video" {
		avc1 := &customAVC1{
			VisualSampleEntry: mp4.VisualSampleEntry{
				Horizresolution: 0x00480000,
				Vertresolution:  0x00480000,
				FrameCount:      1,
				Depth:           0x0018,
				Width:           targetTrack.Width,
				Height:          targetTrack.Height,
			},
		}
		avc1.DataReferenceIndex = 1
		w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'a', 'v', 'c', '1'}})
		mp4.Marshal(w, avc1, mp4.Context{})
		w.Write(targetTrack.CodecBox)
		w.EndBox()
	} else {
		mp4a := &customMP4A{
			AudioSampleEntry: mp4.AudioSampleEntry{
				ChannelCount: 2,
				SampleSize:   16,
				SampleRate:   targetTrack.Timescale << 16,
			},
		}
		mp4a.DataReferenceIndex = 1
		w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'p', '4', 'a'}})
		mp4.Marshal(w, mp4a, mp4.Context{})
		w.Write(targetTrack.CodecBox)
		w.EndBox()
	}
	w.EndBox() // stsd

	// stts (empty)
	stts := &mp4.Stts{}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 't', 's'}})
	mp4.Marshal(w, stts, mp4.Context{})
	w.EndBox()

	// stsc (empty)
	stsc := &mp4.Stsc{}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 's', 'c'}})
	mp4.Marshal(w, stsc, mp4.Context{})
	w.EndBox()

	// stsz (empty)
	stsz := &mp4.Stsz{}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 's', 'z'}})
	mp4.Marshal(w, stsz, mp4.Context{})
	w.EndBox()

	// stco (empty)
	stco := &mp4.Stco{}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 'c', 'o'}})
	mp4.Marshal(w, stco, mp4.Context{})
	w.EndBox()

	w.EndBox() // stbl
	w.EndBox() // minf
	w.EndBox() // mdia
	w.EndBox() // trak

	// mvex
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'v', 'e', 'x'}})
	trex := &mp4.Trex{
		TrackID:                       targetTrack.ID,
		DefaultSampleDescriptionIndex: 1,
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'r', 'e', 'x'}})
	mp4.Marshal(w, trex, mp4.Context{})
	w.EndBox()
	w.EndBox() // mvex

	w.EndBox() // moov

	_, err := writer.Write(mws.buf)
	return err
}

func MuxMediaSegment(ctx context.Context, meta *mp4_parser.MovieMetadata, segment hls.HLSSegment, trackType string, segmentNum int, filePath string, writer io.Writer) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open source MP4: %w", err)
	}
	defer file.Close()

	var targetTrack *mp4_parser.Track
	for _, track := range meta.Tracks {
		if track.Type == trackType {
			targetTrack = track
			break
		}
	}
	if targetTrack == nil {
		return fmt.Errorf("track type %s not found in metadata", trackType)
	}

	startIdx := segment.StartSampleV
	endIdx := segment.EndSampleV
	if trackType == "audio" {
		startIdx = segment.StartSampleA
		endIdx = segment.EndSampleA
	}

	if startIdx >= len(targetTrack.Samples) {
		return fmt.Errorf("start sample index out of range")
	}
	if endIdx > len(targetTrack.Samples) {
		endIdx = len(targetTrack.Samples)
	}

	// 1. Write styp
	mws := &memoryWriteSeeker{}
	w := mp4.NewWriter(mws)

	styp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'m', 's', 'd', 'h'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}},
			{CompatibleBrand: [4]byte{'m', 's', 'i', 'x'}},
		},
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'s', 't', 'y', 'p'}})
	mp4.Marshal(w, styp, mp4.Context{})
	w.EndBox()

	// 2. Prepare TRUN entries
	samplesCount := endIdx - startIdx
	trunEntries := make([]mp4.TrunEntry, samplesCount)
	totalMediaSize := uint32(0)

	hasCTTS := false
	for i := 0; i < samplesCount; i++ {
		s := targetTrack.Samples[startIdx+i]
		dur := uint32(0)
		if startIdx+i+1 < len(targetTrack.Samples) {
			dur = uint32(targetTrack.Samples[startIdx+i+1].DTS - s.DTS)
		} else {
			dur = uint32(targetTrack.Duration - s.DTS)
		}

		offset := int32(s.PTS - s.DTS)
		if offset != 0 {
			hasCTTS = true
		}

		trunEntries[i] = mp4.TrunEntry{
			SampleDuration:                dur,
			SampleSize:                    s.Size,
			SampleCompositionTimeOffsetV1: offset,
		}
		totalMediaSize += s.Size
	}

	// First pass for moof serialization
	moofBytes, err := serializeMoof(targetTrack.ID, segmentNum, targetTrack.Samples[startIdx].DTS, trunEntries, hasCTTS, 0)
	if err != nil {
		return err
	}

	// Second pass: Update DataOffset (size of moof + 8 bytes of mdat header)
	dataOffset := int32(len(moofBytes) + 8)
	moofBytes, err = serializeMoof(targetTrack.ID, segmentNum, targetTrack.Samples[startIdx].DTS, trunEntries, hasCTTS, dataOffset)
	if err != nil {
		return err
	}

	// 3. Write moof
	_, _ = mws.Write(moofBytes)

	// 4. Write mdat
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'd', 'a', 't'}})
	// Write raw sample payload bytes
	for i := 0; i < samplesCount; i++ {
		s := targetTrack.Samples[startIdx+i]
		rawBytes := make([]byte, s.Size)
		if _, err := file.ReadAt(rawBytes, s.Offset); err != nil {
			return fmt.Errorf("failed to read sample payload: %w", err)
		}
		w.Write(rawBytes)
	}
	w.EndBox() // End mdat

	_, err = writer.Write(mws.buf)
	return err
}

func serializeMoof(trackID uint32, seqNum int, baseDTS int64, entries []mp4.TrunEntry, hasCTTS bool, dataOffset int32) ([]byte, error) {
	mws := &memoryWriteSeeker{}
	w := mp4.NewWriter(mws)

	// Start moof
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'o', 'o', 'f'}})

	// mfhd
	mfhd := &mp4.Mfhd{
		SequenceNumber: uint32(seqNum),
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'m', 'f', 'h', 'd'}})
	mp4.Marshal(w, mfhd, mp4.Context{})
	w.EndBox()

	// Start traf
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'r', 'a', 'f'}})

	// tfhd
	tfhd := &mp4.Tfhd{
		TrackID: trackID,
	}
	tfhd.Flags[0] = 0x02 // default-base-is-moof
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'f', 'h', 'd'}})
	mp4.Marshal(w, tfhd, mp4.Context{})
	w.EndBox()

	// tfdt
	tfdt := &mp4.Tfdt{
		BaseMediaDecodeTimeV1: uint64(baseDTS),
	}
	tfdt.Version = 1
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'f', 'd', 't'}})
	mp4.Marshal(w, tfdt, mp4.Context{})
	w.EndBox()

	// trun
	trun := &mp4.Trun{
		SampleCount: uint32(len(entries)),
		DataOffset:  dataOffset,
		Entries:     entries,
	}
	trun.Version = 1     // Enable version 1 to support signed composition offsets (SampleCompositionTimeOffsetV1)
	trun.Flags[1] = 0x03 // sample-duration-present | sample-size-present
	trun.Flags[2] = 0x01 // data-offset-present
	if hasCTTS {
		trun.Flags[1] |= 0x08 // sample-composition-time-offsets-present
	}
	w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType{'t', 'r', 'u', 'n'}})
	mp4.Marshal(w, trun, mp4.Context{})
	w.EndBox()

	w.EndBox() // End traf
	w.EndBox() // End moof

	return mws.buf, nil
}
