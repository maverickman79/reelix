package service_test

import (
	"testing"

	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/service"
)

// wholphinCapabilities is what the SK1 actually sent, taken from the recorded
// PlaybackInfo body in the Step 0 capture.
var wholphinCapabilities = service.DeviceCapabilities{
	Containers: []string{"asf", "dash", "hls", "m4v", "mkv", "mov", "mp4",
		"ogm", "ogv", "ts", "vob", "webm", "wmv", "xvid"},
	VideoCodecs: []string{"av1", "h264", "hevc", "mpeg", "mpeg2video", "vc1", "vp8", "vp9"},
	AudioCodecs: []string{"aac", "aac_latm", "ac3", "alac", "dca", "dts", "eac3",
		"flac", "mlp", "mp2", "mp3", "opus", "pcm_alaw", "pcm_mulaw", "pcm_s16le",
		"pcm_s20le", "pcm_s24le", "truehd", "vorbis"},
}

// detailWith builds an item detail with one video and one audio stream.
func detailWith(container, video, audio string) service.ItemDetail {
	file := domain.MediaFile{Filename: "movie.mkv"}
	if container != "" {
		file.Container = &container
	}

	detail := service.ItemDetail{
		Item: domain.MediaItem{Title: "A Film"},
		File: &file,
	}
	if video != "" {
		detail.Streams = append(detail.Streams,
			domain.MediaStream{StreamIndex: 0, Kind: domain.StreamKindVideo, Codec: &video})
	}
	if audio != "" {
		detail.Streams = append(detail.Streams,
			domain.MediaStream{StreamIndex: 1, Kind: domain.StreamKindAudio, Codec: &audio})
	}
	return detail
}

// TestDecideDirectPlay covers the only playback decision 0.0.1 makes.
func TestDecideDirectPlay(t *testing.T) {
	playback := service.NewPlaybackService(nil)

	tests := []struct {
		name       string
		detail     service.ItemDetail
		caps       service.DeviceCapabilities
		directPlay bool
	}{
		{
			// The gate: Idiocracy on the SK1.
			name:       "matroska h264 eac3 on the real device profile",
			detail:     detailWith("matroska,webm", "h264", "eac3"),
			caps:       wholphinCapabilities,
			directPlay: true,
		},
		{
			// ffprobe names an mp4 by every extension sharing the format.
			// The device lists "mp4" and "mov" separately, so the file is
			// playable even though the whole string matches nothing.
			name:       "an mp4's comma-separated format list matches on one name",
			detail:     detailWith("mov,mp4,m4a,3gp,3g2,mj2", "h264", "aac"),
			caps:       wholphinCapabilities,
			directPlay: true,
		},
		{
			name:       "the 4K remux, hevc and dts",
			detail:     detailWith("matroska,webm", "hevc", "dts"),
			caps:       wholphinCapabilities,
			directPlay: true,
		},
		{
			name:       "a container the device does not list",
			detail:     detailWith("avi", "h264", "eac3"),
			caps:       wholphinCapabilities,
			directPlay: false,
		},
		{
			name:       "a video codec the device does not list",
			detail:     detailWith("matroska,webm", "theora", "eac3"),
			caps:       wholphinCapabilities,
			directPlay: false,
		},
		{
			name:       "an audio codec the device does not list",
			detail:     detailWith("matroska,webm", "h264", "ape"),
			caps:       wholphinCapabilities,
			directPlay: false,
		},
		{
			// Permissive on missing information: direct play is the only
			// thing Reelix can offer, so a refusal helps nobody.
			name:       "no profile at all",
			detail:     detailWith("matroska,webm", "h264", "eac3"),
			caps:       service.DeviceCapabilities{},
			directPlay: true,
		},
		{
			name:       "an unprobed container is not held against the file",
			detail:     detailWith("", "h264", "eac3"),
			caps:       wholphinCapabilities,
			directPlay: true,
		},
		{
			name:       "an unprobed codec is not held against the file",
			detail:     detailWith("matroska,webm", "", ""),
			caps:       wholphinCapabilities,
			directPlay: true,
		},
		{
			name:       "casing and spacing do not matter",
			detail:     detailWith("Matroska,WebM", "H264", "EAC3"),
			caps:       service.DeviceCapabilities{Containers: []string{" mkv ", "matroska"}, VideoCodecs: []string{"h264"}},
			directPlay: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := playback.Decide(tt.detail, tt.caps)
			if got.DirectPlay != tt.directPlay {
				t.Errorf("DirectPlay = %v, want %v (reason: %s)",
					got.DirectPlay, tt.directPlay, got.Reason)
			}
			if got.Reason == "" {
				t.Error("the decision states no reason")
			}
		})
	}
}

// TestDecideWithoutAFile checks an item a scan left half-written.
func TestDecideWithoutAFile(t *testing.T) {
	playback := service.NewPlaybackService(nil)

	decision := playback.Decide(service.ItemDetail{Item: domain.MediaItem{Title: "Orphan"}},
		wholphinCapabilities)

	if decision.DirectPlay {
		t.Error("an item with no file was declared playable")
	}
}

// TestEvaluateResumeThresholds pins what counts as "in progress".
//
// The first two cases are the reference server's own observed behaviour, taken
// from the Step 0 capture: it was watched to 2.5% of Idiocracy and 0.24% of
// Congo, and in both cases reported a resume position of zero and an empty
// Continue Watching list. Reelix has to agree, or a film someone sampled for
// two minutes clutters their home screen forever.
func TestEvaluateResumeThresholds(t *testing.T) {
	const (
		idiocracy = 5050.4
		congo     = 6509.5
	)

	tests := []struct {
		name          string
		position      float64
		runtime       float64
		wantResume    float64
		wantCompleted bool
	}{
		{
			name:     "the capture's Idiocracy stop, 2.5% in",
			position: 126.389, runtime: idiocracy,
		},
		{
			name:     "the capture's Congo stop, 0.24% in",
			position: 15.474, runtime: congo,
		},
		{
			name:     "half way through is resumable",
			position: idiocracy / 2, runtime: idiocracy,
			wantResume: idiocracy / 2,
		},
		{
			name:     "the closing credits count as watched",
			position: idiocracy * 0.95, runtime: idiocracy,
			wantCompleted: true,
		},
		{
			name:     "the very end counts as watched",
			position: idiocracy, runtime: idiocracy,
			wantCompleted: true,
		},
		{
			// Exactly at the bound, which is in.
			name:     "exactly the lower threshold",
			position: idiocracy * 0.05, runtime: idiocracy,
			wantResume: idiocracy * 0.05,
		},
		{
			// Exactly at the bound, which is not yet finished.
			name:     "exactly the upper threshold",
			position: idiocracy * 0.90, runtime: idiocracy,
			wantResume: idiocracy * 0.90,
		},
		{
			name:     "just under the lower threshold",
			position: idiocracy * 0.049, runtime: idiocracy,
		},
		{
			// The runtime floor gates resuming only.
			name:     "a four-minute item is never resumable",
			position: 120, runtime: 240,
		},
		{
			name:     "but a four-minute item watched to the end is played",
			position: 235, runtime: 240,
			wantCompleted: true,
		},
		{
			name:     "an unprobed file has no fraction to judge",
			position: 600, runtime: 0,
		},
		{
			name:     "the start of a film is not progress",
			position: 0, runtime: idiocracy,
		},
		{
			name:     "a negative position is not progress",
			position: -5, runtime: idiocracy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.Evaluate(tt.position, tt.runtime)

			if got.Completed != tt.wantCompleted {
				t.Errorf("Completed = %v, want %v", got.Completed, tt.wantCompleted)
			}
			if got.ResumePosition != tt.wantResume {
				t.Errorf("ResumePosition = %v, want %v", got.ResumePosition, tt.wantResume)
			}
			// A finished item must not also offer a resume point in the
			// credits, and an in-progress one must have somewhere to resume.
			if got.Completed && got.ResumePosition != 0 {
				t.Errorf("a completed item kept a resume position of %v", got.ResumePosition)
			}
		})
	}
}
