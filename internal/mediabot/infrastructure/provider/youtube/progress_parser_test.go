package youtube

import "testing"

func TestParseProgressLine(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantOK   bool
		wantPct  float64
		wantSize string
		wantETA  string
	}{
		{
			name:     "standard line with ETA",
			line:     "[download]  45.2% of 5.23MiB at 1.2MiB/s ETA 00:03",
			wantOK:   true,
			wantPct:  45.2,
			wantSize: "5.23MiB",
			wantETA:  "00:03",
		},
		{
			name:     "integer percent",
			line:     "[download]  50% of 5.23MiB at 1.2MiB/s ETA 00:03",
			wantOK:   true,
			wantPct:  50,
			wantSize: "5.23MiB",
			wantETA:  "00:03",
		},
		{
			name:     "100 percent with in",
			line:     "[download] 100% of 5.23MiB in 00:04",
			wantOK:   true,
			wantPct:  100,
			wantSize: "5.23MiB",
			wantETA:  "00:04",
		},
		{
			name:     "100 percent without trailing",
			line:     "[download] 100% of 5.23MiB",
			wantOK:   true,
			wantPct:  100,
			wantSize: "5.23MiB",
		},
		{
			name:     "estimated size with tilde",
			line:     "[download]  12.3% of ~5.23MiB at 800KiB/s ETA 00:06",
			wantOK:   true,
			wantPct:  12.3,
			wantSize: "5.23MiB",
			wantETA:  "00:06",
		},
		{
			name:     "unknown speed and eta on first tick",
			line:     "[download]   0.0% of   5.23MiB at  Unknown B/s ETA Unknown",
			wantOK:   true,
			wantPct:  0,
			wantSize: "5.23MiB",
			wantETA:  "",
		},
		{
			name:   "destination banner is not progress",
			line:   "[download] Destination: foo.webm",
			wantOK: false,
		},
		{
			name:   "already downloaded",
			line:   "[download] foo.webm has already been downloaded",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "unrelated stderr",
			line:   "WARNING: age-restricted",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseProgressLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Percent != tc.wantPct {
				t.Errorf("percent = %v, want %v", got.Percent, tc.wantPct)
			}
			if got.TotalSize != tc.wantSize {
				t.Errorf("size = %q, want %q", got.TotalSize, tc.wantSize)
			}
			if got.ETA != tc.wantETA {
				t.Errorf("eta = %q, want %q", got.ETA, tc.wantETA)
			}
		})
	}
}
