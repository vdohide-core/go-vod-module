package mp4

import (
	"testing"
)

func TestParseSampleMP4(t *testing.T) {
	// sample.mp4 is located in ../../testdata/sample.mp4 relative to internal/mp4
	meta, err := Parse("../../testdata/sample.mp4")
	if err != nil {
		t.Fatalf("Failed to parse sample.mp4: %v", err)
	}

	if meta.Timescale != 1000 {
		t.Errorf("Expected movie timescale 1000, got %d", meta.Timescale)
	}

	if len(meta.Tracks) != 2 {
		t.Fatalf("Expected 2 tracks, got %d", len(meta.Tracks))
	}

	videoTrack := meta.Tracks[0]
	if videoTrack.Type != "video" {
		t.Errorf("Expected track 0 type video, got %s", videoTrack.Type)
	}
	if videoTrack.Codec != "avc1" {
		t.Errorf("Expected codec avc1, got %s", videoTrack.Codec)
	}
	if len(videoTrack.Samples) != 10 {
		t.Errorf("Expected 10 video samples, got %d", len(videoTrack.Samples))
	}

	firstSample := videoTrack.Samples[0]
	if firstSample.Offset != 48 {
		t.Errorf("Expected first sample offset 48, got %d", firstSample.Offset)
	}
	if firstSample.Size != 3679 {
		t.Errorf("Expected first sample size 3679, got %d", firstSample.Size)
	}
	if firstSample.PTS != 2048 {
		t.Errorf("Expected first sample PTS 2048, got %d", firstSample.PTS)
	}
	if firstSample.DTS != 0 {
		t.Errorf("Expected first sample DTS 0, got %d", firstSample.DTS)
	}
	if !firstSample.IsKeyframe {
		t.Errorf("Expected first sample to be a keyframe")
	}

	audioTrack := meta.Tracks[1]
	if audioTrack.Type != "audio" {
		t.Errorf("Expected track 1 type audio, got %s", audioTrack.Type)
	}
	if audioTrack.Codec != "mp4a" {
		t.Errorf("Expected codec mp4a, got %s", audioTrack.Codec)
	}
	if len(audioTrack.Samples) != 44 {
		t.Errorf("Expected 44 audio samples, got %d", len(audioTrack.Samples))
	}
}
