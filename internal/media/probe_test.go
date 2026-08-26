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
            "width": 1920,
            "height": 1080,
            "bit_rate": "8000000",
            "tags": { "language": "eng" }
        },
        {
            "index": 1,
            "codec_name": "ac3",
            "codec_type": "audio",
            "channels": 6,
            "bit_rate": "640000",
            "tags": { "language": "eng" }
        },
        {
            "index": 2,
            "codec_name": "subrip",
            "codec_type": "subtitle",
            "tags": { "language": "eng" }
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

	subtitle := got.Streams[2]
	if subtitle.Kind != "subtitle" || subtitle.Codec != "subrip" {
		t.Errorf("stream 2 = %+v", subtitle)
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
}

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
