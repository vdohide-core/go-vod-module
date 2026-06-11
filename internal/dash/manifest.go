package dash

import (
	"fmt"
	"strings"

	"go-vod-module/internal/hls"
	"go-vod-module/internal/mp4"
)

func GenerateManifest(meta *mp4.MovieMetadata, segments []hls.HLSSegment) (string, error) {
	var videoTrack *mp4.Track
	var audioTrack *mp4.Track
	totalSize := int64(0)
	for _, track := range meta.Tracks {
		if track.Type == "video" {
			videoTrack = track
		} else if track.Type == "audio" {
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

	videoBandwidth := int64(1200000) // 1.2 Mbps fallback
	audioBandwidth := int64(128000)  // 128 kbps fallback

	if videoTrack != nil {
		videoSize := int64(0)
		for _, s := range videoTrack.Samples {
			videoSize += int64(s.Size)
		}
		if movieDurationSec > 0 {
			videoBandwidth = int64(float64(videoSize*8) / movieDurationSec)
		}
	}
	if audioTrack != nil {
		audioSize := int64(0)
		for _, s := range audioTrack.Samples {
			audioSize += int64(s.Size)
		}
		if movieDurationSec > 0 {
			audioBandwidth = int64(float64(audioSize*8) / movieDurationSec)
		}
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	sb.WriteString(`<MPD xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"` + "\n")
	sb.WriteString(`     xmlns="urn:mpeg:dash:schema:mpd:2011"` + "\n")
	sb.WriteString(`     xsi:schemaLocation="urn:mpeg:dash:schema:mpd:2011 DASH-MPD.xsd"` + "\n")
	sb.WriteString(`     profiles="urn:mpeg:dash:profile:isoff-static:2011"` + "\n")
	sb.WriteString(`     type="static"` + "\n")
	sb.WriteString(fmt.Sprintf(`     mediaPresentationDuration="PT%.3fS"`+"\n", movieDurationSec))

	maxSegDur := 0.0
	for _, seg := range segments {
		if seg.DurationSec > maxSegDur {
			maxSegDur = seg.DurationSec
		}
	}
	sb.WriteString(fmt.Sprintf(`     maxSegmentDuration="PT%.3fS"`+"\n", maxSegDur))
	sb.WriteString(`     minBufferTime="PT2.000S">` + "\n")
	sb.WriteString(`  <Period id="0" start="PT0S">` + "\n")

	// 1. Video Adaptation Set
	if videoTrack != nil {
		codecStr := "avc1.64001f"
		if len(videoTrack.CodecBox) > 8 {
			profile := videoTrack.CodecBox[9]
			compat := videoTrack.CodecBox[10]
			level := videoTrack.CodecBox[11]
			codecStr = fmt.Sprintf("avc1.%02x%02x%02x", profile, compat, level)
		}
		sb.WriteString(fmt.Sprintf(`    <AdaptationSet id="0" mimeType="video/mp4" codecs="%s" width="%d" height="%d" frameRate="25" segmentAlignment="true" startWithSAP="1">`+"\n",
			codecStr, videoTrack.Width, videoTrack.Height))
		sb.WriteString(`      <SegmentTemplate timescale="` + fmt.Sprint(videoTrack.Timescale) + `" initialization="init-v1.mp4" media="seg-$Number$-v1.m4s">` + "\n")
		sb.WriteString(`        <SegmentTimeline>` + "\n")

		for idx, seg := range segments {
			startDTS := videoTrack.Samples[seg.StartSampleV].DTS
			var dur int64
			if idx < len(segments)-1 {
				nextStartDTS := videoTrack.Samples[segments[idx+1].StartSampleV].DTS
				dur = nextStartDTS - startDTS
			} else {
				dur = videoTrack.Duration - startDTS
			}

			tAttr := ""
			if idx == 0 {
				tAttr = fmt.Sprintf(` t="%d"`, startDTS)
			}
			sb.WriteString(fmt.Sprintf(`          <S%s d="%d" />`+"\n", tAttr, dur))
		}

		sb.WriteString(`        </SegmentTimeline>` + "\n")
		sb.WriteString(`      </SegmentTemplate>` + "\n")
		sb.WriteString(fmt.Sprintf(`      <Representation id="v1" bandwidth="%d" />`+"\n", videoBandwidth))
		sb.WriteString(`    </AdaptationSet>` + "\n")
	}

	// 2. Audio Adaptation Set
	if audioTrack != nil {
		sb.WriteString(fmt.Sprintf(`    <AdaptationSet id="1" mimeType="audio/mp4" codecs="mp4a.40.2" audioSamplingRate="%d" segmentAlignment="true" startWithSAP="1">`+"\n",
			audioTrack.Timescale))
		sb.WriteString(`      <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2" />` + "\n")
		sb.WriteString(`      <SegmentTemplate timescale="` + fmt.Sprint(audioTrack.Timescale) + `" initialization="init-a1.mp4" media="seg-$Number$-a1.m4s">` + "\n")
		sb.WriteString(`        <SegmentTimeline>` + "\n")

		for idx, seg := range segments {
			if seg.StartSampleA >= len(audioTrack.Samples) {
				continue
			}
			startDTS := audioTrack.Samples[seg.StartSampleA].DTS
			var dur int64
			if idx < len(segments)-1 {
				nextStartIdx := segments[idx+1].StartSampleA
				if nextStartIdx < len(audioTrack.Samples) {
					nextStartDTS := audioTrack.Samples[nextStartIdx].DTS
					dur = nextStartDTS - startDTS
				} else {
					dur = audioTrack.Duration - startDTS
				}
			} else {
				dur = audioTrack.Duration - startDTS
			}

			tAttr := ""
			if idx == 0 {
				tAttr = fmt.Sprintf(` t="%d"`, startDTS)
			}
			sb.WriteString(fmt.Sprintf(`          <S%s d="%d" />`+"\n", tAttr, dur))
		}

		sb.WriteString(`        </SegmentTimeline>` + "\n")
		sb.WriteString(`      </SegmentTemplate>` + "\n")
		sb.WriteString(fmt.Sprintf(`      <Representation id="a1" bandwidth="%d" />`+"\n", audioBandwidth))
		sb.WriteString(`    </AdaptationSet>` + "\n")
	}

	sb.WriteString(`  </Period>` + "\n")
	sb.WriteString(`</MPD>` + "\n")

	return sb.String(), nil
}
