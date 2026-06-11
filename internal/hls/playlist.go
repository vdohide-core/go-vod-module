package hls

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"go-vod-module/internal/mp4"
)

type HLSSegment struct {
	Index        int
	DurationSec  float64
	StartSampleV int // Start sample index for video
	EndSampleV   int // End sample index for video (exclusive)
	StartSampleA int // Start sample index for audio
	EndSampleA   int // End sample index for audio (exclusive)
}

func GenerateSegments(meta *mp4.MovieMetadata, targetDurMs int, alignToKeyFrames bool) ([]HLSSegment, error) {
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

	if videoTrack == nil && audioTrack == nil {
		return nil, fmt.Errorf("no video or audio tracks found")
	}

	var segments []HLSSegment

	if videoTrack != nil && len(videoTrack.Samples) > 0 {
		vSamples := videoTrack.Samples
		timescale := float64(videoTrack.Timescale)

		startV := 0
		segIdx := 0

		var segmentLimitMs int64 = int64(targetDurMs)
		segmentLimitDTS := int64(math.Round(float64(segmentLimitMs) * timescale / 1000.0))

		for i := 0; i < len(vSamples); i++ {
			sample := vSamples[i]
			accumDTS := sample.DTS - vSamples[0].DTS
			isLast := i == len(vSamples)-1

			for accumDTS >= segmentLimitDTS && !isLast && (!alignToKeyFrames || sample.IsKeyframe) {
				endV := i

				// Find aligned audio samples
				startA := 0
				endA := 0
				if audioTrack != nil {
					startTimeSec := float64(vSamples[startV].DTS) / timescale
					endTimeSec := float64(vSamples[endV].DTS) / timescale

					startA = findAudioSampleIdx(audioTrack, startTimeSec)
					endA = findAudioSampleIdx(audioTrack, endTimeSec)
				}

				segDur := float64(vSamples[endV].DTS-vSamples[startV].DTS) / timescale

				segments = append(segments, HLSSegment{
					Index:        segIdx,
					DurationSec:  segDur,
					StartSampleV: startV,
					EndSampleV:   endV,
					StartSampleA: startA,
					EndSampleA:   endA,
				})

				startV = endV
				segIdx++
				segmentLimitMs += int64(targetDurMs)
				segmentLimitDTS = int64(math.Round(float64(segmentLimitMs) * timescale / 1000.0))
			}

			if isLast {
				endV := len(vSamples)
				startA := 0
				endA := 0
				if audioTrack != nil {
					startTimeSec := float64(vSamples[startV].DTS) / timescale
					startA = findAudioSampleIdx(audioTrack, startTimeSec)
					endA = len(audioTrack.Samples)
				}

				segDur := float64(videoTrack.Duration)/timescale - float64(vSamples[startV].DTS)/timescale

				segments = append(segments, HLSSegment{
					Index:        segIdx,
					DurationSec:  segDur,
					StartSampleV: startV,
					EndSampleV:   endV,
					StartSampleA: startA,
					EndSampleA:   endA,
				})
			}
		}
	} else if audioTrack != nil && len(audioTrack.Samples) > 0 {
		// Audio only
		aSamples := audioTrack.Samples
		timescale := float64(audioTrack.Timescale)

		startA := 0
		segIdx := 0

		var segmentLimitMs int64 = int64(targetDurMs)
		segmentLimitDTS := int64(math.Round(float64(segmentLimitMs) * timescale / 1000.0))

		for i := 0; i < len(aSamples); i++ {
			sample := aSamples[i]
			accumDTS := sample.DTS - aSamples[0].DTS
			isLast := i == len(aSamples)-1

			for accumDTS >= segmentLimitDTS && !isLast {
				endA := i
				segDur := float64(aSamples[endA].DTS-aSamples[startA].DTS) / timescale

				segments = append(segments, HLSSegment{
					Index:        segIdx,
					DurationSec:  segDur,
					StartSampleV: 0,
					EndSampleV:   0,
					StartSampleA: startA,
					EndSampleA:   endA,
				})

				startA = endA
				segIdx++
				segmentLimitMs += int64(targetDurMs)
				segmentLimitDTS = int64(math.Round(float64(segmentLimitMs) * timescale / 1000.0))
			}

			if isLast {
				endA := len(aSamples)
				segDur := float64(audioTrack.Duration)/timescale - float64(aSamples[startA].DTS)/timescale

				segments = append(segments, HLSSegment{
					Index:        segIdx,
					DurationSec:  segDur,
					StartSampleV: 0,
					EndSampleV:   0,
					StartSampleA: startA,
					EndSampleA:   endA,
				})
			}
		}
	}

	return segments, nil
}

func findAudioSampleIdx(track *mp4.Track, timeSec float64) int {
	targetDTS := int64(timeSec * float64(track.Timescale))
	return sort.Search(len(track.Samples), func(i int) bool {
		return track.Samples[i].DTS >= targetDTS
	})
}

func GenerateMediaPlaylist(segments []HLSSegment, segmentNamePattern string) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	sb.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	sb.WriteString("#EXT-X-MEDIA-SEQUENCE:1\n")

	maxDur := 0.0
	for _, seg := range segments {
		if seg.DurationSec > maxDur {
			maxDur = seg.DurationSec
		}
	}
	targetDur := int(math.Ceil(maxDur))
	sb.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", targetDur))

	for _, seg := range segments {
		sb.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", seg.DurationSec))
		sb.WriteString(fmt.Sprintf(segmentNamePattern+"\n", seg.Index+1))
	}

	sb.WriteString("#EXT-X-ENDLIST\n")
	return sb.String()
}

func GenerateMasterPlaylist(meta *mp4.MovieMetadata, mediaPlaylistURI string) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")

	var videoTrack *mp4.Track
	var audioTrack *mp4.Track
	totalSize := int64(0)
	for _, track := range meta.Tracks {
		switch track.Type {
		case "video":
			videoTrack = track
		case "audio":
			audioTrack = track
		}
		for _, s := range track.Samples {
			totalSize += int64(s.Size)
		}
	}

	movieDurationSec := float64(meta.Duration) / float64(meta.Timescale)
	if movieDurationSec <= 0 && videoTrack != nil {
		movieDurationSec = float64(videoTrack.Duration) / float64(videoTrack.Timescale)
	}

	bandwidth := int64(1500000) // fallback 1.5 Mbps
	if movieDurationSec > 0 {
		bandwidth = int64(float64(totalSize*8) / movieDurationSec)
	}

	// Codecs configuration
	codecs := []string{}
	resolutionStr := ""

	if videoTrack != nil {
		// Default avc1 profile/level (e.g. avc1.64001f for High 3.1)
		codecStr := "avc1.64001f"
		// Try to parse from avcC box if available
		if len(videoTrack.CodecBox) > 8 {
			// Offset of payload in avcC is 8. Payload: profile=offset+1, compat=offset+2, level=offset+3
			profile := videoTrack.CodecBox[9]
			compat := videoTrack.CodecBox[10]
			level := videoTrack.CodecBox[11]
			codecStr = fmt.Sprintf("avc1.%02x%02x%02x", profile, compat, level)
		}
		codecs = append(codecs, codecStr)
		resolutionStr = fmt.Sprintf(",RESOLUTION=%dx%d", videoTrack.Width, videoTrack.Height)
	}

	if audioTrack != nil {
		codecs = append(codecs, "mp4a.40.2") // AAC-LC
	}

	codecsAttr := ""
	if len(codecs) > 0 {
		codecsAttr = fmt.Sprintf(`,CODECS="%s"`, strings.Join(codecs, ","))
	}

	sb.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d%s%s\n", bandwidth, resolutionStr, codecsAttr))
	sb.WriteString(mediaPlaylistURI + "\n")

	return sb.String()
}
