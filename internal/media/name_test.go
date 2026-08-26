package media

import "testing"

func TestParseName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		title string
		year  int // 0 means no year expected
	}{
		// The six files in the real test library. Five are scene-style dotted
		// names; only one uses the Title (Year) form the MVP describes.
		{
			name:  "scene dotted name",
			input: "Idiocracy.2006.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX.mkv",
			title: "Idiocracy", year: 2006,
		},
		{
			name:  "scene name with multi-word title",
			input: "Fight.Club.1999.UHD.BluRay.2160p.DTS-HD.MA.5.1.DV.HDR10P.HEVC.HYBRID.REMUX-FraMeSToR",
			title: "Fight Club", year: 1999,
		},
		{
			name:  "scene name with remastered tag after year",
			input: "Congo.1995.Remastered.1080p.BluRay.x264-OFT",
			title: "Congo", year: 1995,
		},
		{
			name:  "spaces already present",
			input: "Gangland 2025 1080p WEB-DL H264-CinemaCity.mp4",
			title: "Gangland", year: 2025,
		},
		{
			name:  "parenthesised year with brackets after",
			input: "The Legend of Aang - The Last Airbender (2026) [INTERNAL] 1080p H.264 English AAC 2.0.mkv",
			title: "The Legend of Aang - The Last Airbender", year: 2026,
		},
		{
			name:  "dotted name with dotted audio spec",
			input: "The.Singers.2026.2160p.NF.WEB-DL.DDP.5.1.H.265-CHDWEB.mkv",
			title: "The Singers", year: 2026,
		},

		// The plain form the MVP names.
		{name: "title and year", input: "Arrival (2016).mkv", title: "Arrival", year: 2016},
		{name: "title and bare year", input: "Arrival 2016.mkv", title: "Arrival", year: 2016},
		{name: "bracketed year", input: "Arrival [2016].mkv", title: "Arrival", year: 2016},

		// A resolution must not be mistaken for a year. This is the reason the
		// pattern is bounded to 19xx/20xx.
		{
			name:  "resolution is not a year",
			input: "Some Movie 1080p BluRay.mkv",
			title: "Some Movie 1080p BluRay",
		},
		{
			name:  "2160p is not a year",
			input: "Another.Movie.2160p.WEB-DL.mkv",
			title: "Another Movie 2160p WEB-DL",
		},

		// No year at all: the whole name becomes the title. An ugly title is
		// recoverable, a missing one is not.
		{name: "no year", input: "Home Video.mkv", title: "Home Video"},
		{name: "no year dotted", input: "Home.Video.mkv", title: "Home Video"},

		// A year in the leading position is far more likely to be the title.
		{name: "year as title", input: "2012 (2009).mkv", title: "2012", year: 2009},
		{name: "year only", input: "1917.mkv", title: "1917"},

		// Punctuation and separators around the title get trimmed.
		{name: "trailing separator", input: "Arrival - (2016).mkv", title: "Arrival", year: 2016},
		{name: "underscores", input: "The_Matrix_1999_1080p.mkv", title: "The Matrix", year: 1999},

		// Directory names have no extension to strip, and may contain dots
		// that are not extensions.
		{
			name:  "directory name",
			input: "Blade Runner 2049 (2017)",
			title: "Blade Runner 2049", year: 2017,
		},

		{name: "empty", input: "", title: ""},
		{name: "extension only", input: ".mkv", title: ".mkv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseName(tt.input)

			if got.Title != tt.title {
				t.Errorf("title = %q, want %q", got.Title, tt.title)
			}

			switch {
			case tt.year == 0 && got.Year != nil:
				t.Errorf("year = %d, want none", *got.Year)
			case tt.year != 0 && got.Year == nil:
				t.Errorf("year = none, want %d", tt.year)
			case tt.year != 0 && *got.Year != tt.year:
				t.Errorf("year = %d, want %d", *got.Year, tt.year)
			}
		})
	}
}

// TestParseNameBladeRunner2049 is the case where the title itself ends in a
// number that looks like a year.
//
// "Blade Runner 2049 (2017)" must not parse as title "Blade Runner", year
// 2049. It works because the parenthesised year is found first only if it
// appears first — here 2049 does, so this documents the actual behaviour
// rather than an aspiration.
func TestParseNameBladeRunner2049(t *testing.T) {
	got := ParseName("Blade Runner 2049 (2017)")

	if got.Title != "Blade Runner 2049" {
		t.Errorf("title = %q, want %q", got.Title, "Blade Runner 2049")
	}
	if got.Year == nil || *got.Year != 2017 {
		t.Errorf("year = %v, want 2017", got.Year)
	}
}
