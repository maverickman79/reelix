package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is what ffprobe reported about one file.
type ProbeResult struct {
	// Container is ffprobe's format_name, e.g. "matroska,webm".
	Container string
	// Duration is nil when ffprobe could not determine one.
	Duration *float64
	Streams  []ProbeStream
}

// ProbeStream is one track within a container.
//
// Width, Height, Profile, Level, PixelFormat and the two frame rates are
// video-only in practice; Channels is audio-only. ffprobe reports the whole
// set for every stream and leaves the inapplicable ones absent, so a nil here
// means ffprobe said nothing rather than that Reelix declined to look.
//
// Language is whatever the container's tag says, including the literal "und".
// Normalising that away would discard the difference between a track tagged
// undefined and a track with no tag at all.
type ProbeStream struct {
	Index    int
	Kind     string
	Codec    string
	Width    *int
	Height   *int
	Channels *int
	BitRate  *int64

	Language    *string
	Title       *string
	Profile     *string
	Level       *int
	PixelFormat *string

	// Both rates are carried because ffprobe reports two different things:
	// r_frame_rate is the container's base rate and avg_frame_rate the
	// measured average. They agree on constant-frame-rate content and
	// diverge on variable, so deriving one from the other would be a guess
	// made at write time that cannot be undone later.
	AverageFrameRate *float64
	RealFrameRate    *float64

	// Dispositions are booleans rather than pointers: ffprobe always reports
	// the object, so "not flagged" is an answer rather than an absence.
	IsDefault         bool
	IsForced          bool
	IsHearingImpaired bool
}

// Prober runs ffprobe against media files.
type Prober struct {
	binary  string
	timeout time.Duration
}

// NewProber returns a Prober invoking the ffprobe at binary.
func NewProber(binary string, timeout time.Duration) *Prober {
	return &Prober{binary: binary, timeout: timeout}
}

// ErrProbeTimeout means ffprobe exceeded its deadline for one file.
var ErrProbeTimeout = errors.New("ffprobe timed out")

// Probe inspects one file.
//
// The call is bounded by the Prober's timeout. A hung probe — a network mount
// that stopped answering, a pathological container — must not stall a scan
// that still has hundreds of files to get through.
func (p *Prober) Probe(ctx context.Context, path string) (ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.binary,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ProbeResult{}, fmt.Errorf("%w after %s", ErrProbeTimeout, p.timeout)
		}
		// ffprobe's stderr says what was actually wrong with the file, which
		// is the whole diagnostic value; without it the operator sees only
		// "exit status 1".
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return ProbeResult{}, fmt.Errorf("ffprobe: %w", err)
		}
		return ProbeResult{}, fmt.Errorf("ffprobe: %s", firstLine(msg))
	}

	return parseProbeOutput(stdout.Bytes())
}

// Version returns the probe binary's first version line, used at startup to
// confirm it is present and executable.
func (p *Prober) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, p.binary, "-version").Output()
	if err != nil {
		return "", fmt.Errorf("running %s: %w", p.binary, err)
	}
	return firstLine(strings.TrimSpace(string(out))), nil
}

// ffprobeOutput mirrors the JSON ffprobe emits.
//
// Numbers arrive as strings in ffprobe's JSON — duration, bit_rate, channels —
// so they are decoded as strings and converted deliberately rather than
// relying on the decoder to guess.
type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Channels  int    `json:"channels"`
		BitRate   string `json:"bit_rate"`

		// Already present in the -show_streams output this package has
		// always asked for; until 0.0.2 they were simply not declared, so
		// the decoder discarded them. The ffprobe invocation is unchanged.
		Profile     string `json:"profile"`
		Level       int    `json:"level"`
		PixelFormat string `json:"pix_fmt"`

		// Rationals, as strings: "24000/1001", or "0/0" when there is none.
		AvgFrameRate  string `json:"avg_frame_rate"`
		RealFrameRate string `json:"r_frame_rate"`

		Tags struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`

		// ffprobe renders these as 0/1 rather than as JSON booleans.
		Disposition struct {
			Default         int `json:"default"`
			Forced          int `json:"forced"`
			HearingImpaired int `json:"hearing_impaired"`
		} `json:"disposition"`
	} `json:"streams"`
}

// parseProbeOutput converts ffprobe's JSON into a ProbeResult.
func parseProbeOutput(raw []byte) (ProbeResult, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return ProbeResult{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}

	result := ProbeResult{Container: out.Format.FormatName}

	if d, err := strconv.ParseFloat(out.Format.Duration, 64); err == nil && d > 0 {
		result.Duration = &d
	}

	for _, s := range out.Streams {
		kind := s.CodecType

		// ffprobe also reports data and attachment streams — fonts embedded in
		// an MKV, timecode tracks. The media_streams CHECK permits video,
		// audio, and subtitle only, so anything else is dropped here rather
		// than failing the insert further down.
		if kind != "video" && kind != "audio" && kind != "subtitle" {
			continue
		}

		stream := ProbeStream{
			Index: s.Index,
			Kind:  kind,
			Codec: s.CodecName,
		}

		if kind == "video" {
			if s.Width > 0 {
				w := s.Width
				stream.Width = &w
			}
			if s.Height > 0 {
				h := s.Height
				stream.Height = &h
			}
		}
		if kind == "audio" && s.Channels > 0 {
			c := s.Channels
			stream.Channels = &c
		}
		if b, err := strconv.ParseInt(s.BitRate, 10, 64); err == nil && b > 0 {
			stream.BitRate = &b
		}

		stream.Language = nonEmptyPtr(s.Tags.Language)
		stream.Title = nonEmptyPtr(s.Tags.Title)
		stream.Profile = nonEmptyPtr(s.Profile)
		stream.PixelFormat = nonEmptyPtr(s.PixelFormat)

		// ffprobe writes -99 for "unknown" and 0 on streams that have no
		// concept of a level. Both become nil: a number stored for either
		// would read downstream as a measurement.
		if s.Level > 0 {
			level := s.Level
			stream.Level = &level
		}

		stream.AverageFrameRate = parseRational(s.AvgFrameRate)
		stream.RealFrameRate = parseRational(s.RealFrameRate)

		stream.IsDefault = s.Disposition.Default == 1
		stream.IsForced = s.Disposition.Forced == 1
		stream.IsHearingImpaired = s.Disposition.HearingImpaired == 1

		result.Streams = append(result.Streams, stream)
	}

	return result, nil
}

// nonEmptyPtr maps "" to nil, so an absent tag is null rather than an empty
// string that reads as a track deliberately named nothing.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseRational converts one of ffprobe's "24000/1001" frame rates.
//
// Returns nil for the "0/0" ffprobe emits on streams with no frame rate, for a
// zero numerator, and for anything unparseable — each of which means the same
// thing to a caller, and none of which is 0 fps.
func parseRational(s string) *float64 {
	num, den, found := strings.Cut(s, "/")
	if !found {
		return nil
	}

	n, err := strconv.ParseFloat(num, 64)
	if err != nil || n <= 0 {
		return nil
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d <= 0 {
		return nil
	}

	rate := n / d
	return &rate
}

// firstLine keeps an error message to a single line, so a multi-line ffprobe
// complaint does not turn a log entry into a paragraph.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
