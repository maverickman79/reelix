package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realFFprobeOutput is ffprobe 7.1.4 output for an H.264/AC3 MKV with an
// embedded subtitle track, trimmed to the fields Reelix reads.
//
// Recorded rather than generated, so the mapping is tested against what
// ffprobe actually emits — numbers as strings included — without needing the
// binary present.
const realFFprobeOutput = `{
    "streams": [
        {
            "index": 0,
            "codec_name": "h264",
            "codec_type": "video",
            "profile": "High",
            "level": 40,
            "pix_fmt": "yuv420p",
            "width": 1920,
            "height": 1080,
            "r_frame_rate": "24000/1001",
            "avg_frame_rate": "24000/1001",
            "bit_rate": "8000000",
            "disposition": { "default": 1, "forced": 0, "hearing_impaired": 0 },
            "tags": { "language": "eng" }
        },
        {
            "index": 1,
            "codec_name": "ac3",
            "codec_type": "audio",
            "level": -99,
            "channels": 6,
            "channel_layout": "5.1(side)",
            "sample_rate": "48000",
            "r_frame_rate": "0/0",
            "avg_frame_rate": "0/0",
            "bit_rate": "640000",
            "disposition": { "default": 1, "forced": 0, "hearing_impaired": 0 },
            "tags": { "language": "eng", "title": "Surround AC3 5.1" }
        },
        {
            "index": 2,
            "codec_name": "subrip",
            "codec_type": "subtitle",
            "level": 0,
            "r_frame_rate": "0/0",
            "avg_frame_rate": "0/0",
            "disposition": { "default": 0, "forced": 1, "hearing_impaired": 1 },
            "tags": { "language": "eng", "title": "SDH" }
        },
        {
            "index": 3,
            "codec_name": "ttf",
            "codec_type": "attachment"
        }
    ],
    "format": {
        "format_name": "matroska,webm",
        "duration": "5340.512000",
        "size": "5255910143",
        "bit_rate": "7873453"
    }
}`

func TestParseProbeOutput(t *testing.T) {
	got, err := parseProbeOutput([]byte(realFFprobeOutput))
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}

	if got.Container != "matroska,webm" {
		t.Errorf("container = %q, want matroska,webm", got.Container)
	}
	if got.Duration == nil || *got.Duration != 5340.512 {
		t.Errorf("duration = %v, want 5340.512", got.Duration)
	}

	// The attachment stream is dropped: media_streams permits video, audio,
	// and subtitle only, so it would fail the CHECK constraint if carried.
	if len(got.Streams) != 3 {
		t.Fatalf("got %d streams, want 3 (the ttf attachment must be dropped)", len(got.Streams))
	}

	video := got.Streams[0]
	if video.Kind != "video" || video.Codec != "h264" {
		t.Errorf("stream 0 = %+v", video)
	}
	if video.Width == nil || *video.Width != 1920 {
		t.Errorf("video width = %v, want 1920", video.Width)
	}
	if video.Height == nil || *video.Height != 1080 {
		t.Errorf("video height = %v, want 1080", video.Height)
	}
	if video.BitRate == nil || *video.BitRate != 8_000_000 {
		t.Errorf("video bit rate = %v, want 8000000", video.BitRate)
	}
	// Channels is audio-only and must stay nil on a video stream, so the
	// column is null rather than zero.
	if video.Channels != nil {
		t.Errorf("video stream carries channels = %v, want nil", *video.Channels)
	}
	if video.Profile == nil || *video.Profile != "High" {
		t.Errorf("video profile = %v, want High", video.Profile)
	}
	if video.Level == nil || *video.Level != 40 {
		t.Errorf("video level = %v, want 40", video.Level)
	}
	if video.PixelFormat == nil || *video.PixelFormat != "yuv420p" {
		t.Errorf("video pixel format = %v, want yuv420p", video.PixelFormat)
	}
	// 24000/1001 is 23.976023..., which is why the rational is carried
	// through as a division rather than rounded at parse time.
	if video.RealFrameRate == nil || *video.RealFrameRate < 23.97 || *video.RealFrameRate > 23.98 {
		t.Errorf("video real frame rate = %v, want ~23.976", video.RealFrameRate)
	}
	if video.AverageFrameRate == nil || *video.AverageFrameRate < 23.97 || *video.AverageFrameRate > 23.98 {
		t.Errorf("video average frame rate = %v, want ~23.976", video.AverageFrameRate)
	}
	if video.Language == nil || *video.Language != "eng" {
		t.Errorf("video language = %v, want eng", video.Language)
	}
	if !video.IsDefault {
		t.Error("video stream is not marked default, but the disposition says it is")
	}

	audio := got.Streams[1]
	if audio.Kind != "audio" || audio.Codec != "ac3" {
		t.Errorf("stream 1 = %+v", audio)
	}
	if audio.Channels == nil || *audio.Channels != 6 {
		t.Errorf("audio channels = %v, want 6", audio.Channels)
	}
	if audio.Width != nil {
		t.Errorf("audio stream carries width = %v, want nil", *audio.Width)
	}
	if audio.Title == nil || *audio.Title != "Surround AC3 5.1" {
		t.Errorf("audio title = %v, want \"Surround AC3 5.1\"", audio.Title)
	}
	// ffprobe writes -99 for an unknown level. Storing it would put a
	// sentinel in a column that is read as a codec level.
	if audio.Level != nil {
		t.Errorf("audio level = %v, want nil (ffprobe reported -99)", *audio.Level)
	}
	// "0/0" is ffprobe saying there is no frame rate, not zero frames.
	if audio.RealFrameRate != nil || audio.AverageFrameRate != nil {
		t.Errorf("audio stream carries a frame rate: real=%v avg=%v",
			audio.RealFrameRate, audio.AverageFrameRate)
	}
	// Stored verbatim, qualifier included. Normalising here would throw the
	// distinction away at the only point that still has it.
	if audio.ChannelLayout == nil || *audio.ChannelLayout != "5.1(side)" {
		t.Errorf("audio channel layout = %v, want \"5.1(side)\" exactly as ffprobe reported it",
			audio.ChannelLayout)
	}
	if audio.SampleRate == nil || *audio.SampleRate != 48000 {
		t.Errorf("audio sample rate = %v, want 48000", audio.SampleRate)
	}
	// Video streams carry neither.
	if video.ChannelLayout != nil || video.SampleRate != nil {
		t.Errorf("video stream carries audio fields: layout=%v rate=%v",
			video.ChannelLayout, video.SampleRate)
	}

	if !audio.IsDefault || audio.IsForced || audio.IsHearingImpaired {
		t.Errorf("audio dispositions = default:%v forced:%v hearing_impaired:%v, want true/false/false",
			audio.IsDefault, audio.IsForced, audio.IsHearingImpaired)
	}

	subtitle := got.Streams[2]
	if subtitle.Kind != "subtitle" || subtitle.Codec != "subrip" {
		t.Errorf("stream 2 = %+v", subtitle)
	}
	if subtitle.Title == nil || *subtitle.Title != "SDH" {
		t.Errorf("subtitle title = %v, want SDH", subtitle.Title)
	}
	// A level of 0 on a subtitle stream is C#-flavoured nothing, not a level.
	if subtitle.Level != nil {
		t.Errorf("subtitle level = %v, want nil (ffprobe reported 0)", *subtitle.Level)
	}
	// The three dispositions must be read independently: an SDH track that
	// arrives merely "forced" is the bug this whole change exists to fix.
	if subtitle.IsDefault {
		t.Error("subtitle marked default, but the disposition says otherwise")
	}
	if !subtitle.IsForced {
		t.Error("subtitle not marked forced, but the disposition says it is")
	}
	if !subtitle.IsHearingImpaired {
		t.Error("subtitle not marked hearing impaired, but the disposition says it is")
	}
}

// TestParseProbeOutputMissingFields checks a sparse container does not produce
// zero values that read as real measurements.
func TestParseProbeOutputMissingFields(t *testing.T) {
	const sparse = `{
        "streams": [{"index": 0, "codec_name": "h264", "codec_type": "video"}],
        "format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2"}
    }`

	got, err := parseProbeOutput([]byte(sparse))
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}

	// An absent duration must be nil, not 0 — "unknown length" and "zero
	// length" are different facts.
	if got.Duration != nil {
		t.Errorf("duration = %v, want nil", *got.Duration)
	}
	if len(got.Streams) != 1 {
		t.Fatalf("got %d streams, want 1", len(got.Streams))
	}
	s := got.Streams[0]
	if s.Width != nil || s.Height != nil || s.BitRate != nil || s.Channels != nil {
		t.Errorf("absent fields became values: %+v", s)
	}
	// A container with no tags, no disposition object and no profile must
	// produce nils and falses rather than empty strings and zeroes.
	if s.Language != nil || s.Title != nil || s.Profile != nil ||
		s.Level != nil || s.PixelFormat != nil ||
		s.ChannelLayout != nil || s.SampleRate != nil {
		t.Errorf("absent metadata became values: %+v", s)
	}
	if s.AverageFrameRate != nil || s.RealFrameRate != nil {
		t.Errorf("absent frame rates became values: %+v", s)
	}
	if s.IsDefault || s.IsForced || s.IsHearingImpaired {
		t.Errorf("absent dispositions became true: %+v", s)
	}
}

// TestParseRational pins the frame-rate forms ffprobe actually emits.
func TestParseRational(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want *float64
	}{
		{"24000/1001", ptr(23.976023976023978)},
		{"25/1", ptr(25.0)},
		{"0/0", nil},  // ffprobe's "this stream has no frame rate"
		{"30/0", nil}, // a denominator of zero is not 30 fps
		{"0/1", nil},  // nor is a numerator of zero 0 fps
		{"", nil},     // absent altogether
		{"25", nil},   // not a rational
		{"a/b", nil},  // unparseable
	} {
		got := parseRational(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("parseRational(%q) = %v, want nil", tc.in, *got)
		case tc.want != nil && got == nil:
			t.Errorf("parseRational(%q) = nil, want %v", tc.in, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Errorf("parseRational(%q) = %v, want %v", tc.in, *got, *tc.want)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestParseProbeOutputInvalidJSON(t *testing.T) {
	if _, err := parseProbeOutput([]byte("not json")); err == nil {
		t.Fatal("parsing garbage returned no error")
	}
}

// TestParseProbeOutputZeroDuration checks a reported duration of zero is
// treated as unknown rather than stored as a real zero-length movie.
func TestParseProbeOutputZeroDuration(t *testing.T) {
	const zero = `{"streams": [], "format": {"format_name": "matroska", "duration": "0.000000"}}`

	got, err := parseProbeOutput([]byte(zero))
	if err != nil {
		t.Fatalf("parseProbeOutput: %v", err)
	}
	if got.Duration != nil {
		t.Errorf("duration = %v, want nil", *got.Duration)
	}
}

// probeBinary returns a usable ffprobe, or skips.
func probeBinary(t *testing.T) string {
	t.Helper()

	path, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not on PATH; skipping probe execution test")
	}
	return path
}

// TestProbeRejectsNonMedia checks a probe failure carries ffprobe's own
// explanation rather than a bare exit status.
func TestProbeRejectsNonMedia(t *testing.T) {
	binary := probeBinary(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "notmedia.mkv")
	if err := os.WriteFile(path, []byte("this is not a media file"), 0o644); err != nil {
		t.Fatalf("writing decoy: %v", err)
	}

	prober := NewProber(binary, 30*time.Second)

	_, err := prober.Probe(context.Background(), path)
	if err == nil {
		t.Fatal("probing a text file returned no error")
	}
	if !strings.Contains(err.Error(), "ffprobe") {
		t.Errorf("error does not mention ffprobe: %v", err)
	}
}

// TestProbeMissingFile checks a vanished file is an error, not a silent empty
// result.
func TestProbeMissingFile(t *testing.T) {
	binary := probeBinary(t)
	prober := NewProber(binary, 30*time.Second)

	if _, err := prober.Probe(context.Background(), t.TempDir()+"/nope.mkv"); err == nil {
		t.Fatal("probing a missing file returned no error")
	}
}

// TestProbeTimeout checks the per-file deadline is enforced.
//
// A zero timeout expires before ffprobe can finish, which is the same code
// path a hung network mount takes.
func TestProbeTimeout(t *testing.T) {
	binary := probeBinary(t)
	prober := NewProber(binary, time.Nanosecond)

	_, err := prober.Probe(context.Background(), binary)
	if err == nil {
		t.Fatal("a nanosecond timeout returned no error")
	}
}

func TestProberVersion(t *testing.T) {
	binary := probeBinary(t)
	prober := NewProber(binary, 30*time.Second)

	version, err := prober.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !strings.Contains(strings.ToLower(version), "ffprobe") {
		t.Errorf("version line does not mention ffprobe: %q", version)
	}
	if strings.Contains(version, "\n") {
		t.Error("version line is not a single line")
	}
}

// TestProberVersionMissingBinary checks a misconfigured path fails clearly.
func TestProberVersionMissingBinary(t *testing.T) {
	prober := NewProber("/nonexistent/ffprobe", time.Second)

	_, err := prober.Version(context.Background())
	if err == nil {
		t.Fatal("a missing binary returned no error")
	}
	if !strings.Contains(err.Error(), "/nonexistent/ffprobe") {
		t.Errorf("error does not name the path: %v", err)
	}
}
