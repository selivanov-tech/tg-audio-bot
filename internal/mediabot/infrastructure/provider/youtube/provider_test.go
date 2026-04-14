package youtube

import "testing"

func TestMatchesURL(t *testing.T) {
	p := New(Config{})
	cases := []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/watch?v=abc", true},
		{"https://youtube.com/watch?v=abc", true},
		{"https://m.youtube.com/watch?v=abc", true},
		{"https://youtu.be/abc", true},
		{"https://www.youtube.com/shorts/abc", true},
		{"http://youtu.be/abc", true},
		{"youtube.com/watch?v=abc", true},
		{"https://example.com/watch?v=abc", false},
		{"https://vimeo.com/123", false},
		{"not a url", false},
		{"", false},
	}
	for _, c := range cases {
		if got := p.MatchesURL(c.url); got != c.want {
			t.Errorf("MatchesURL(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"  hello  ", "hello"},
		{"a/b\\c", "a_b_c"},
		{"a\x00b", "a_b"},
		{"a\tb\nc", "abc"},
		{"...hidden...", "hidden"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeFilename(c.in); got != c.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
