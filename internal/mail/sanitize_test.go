package mail

import (
	"strings"
	"testing"
)

func TestSanitizeHTML(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		contains []string
		absent   []string
	}{
		{
			name:     "script element and its text are removed",
			in:       `<p>a</p><script>alert(1)</script>`,
			contains: []string{"<p>a</p>"},
			absent:   []string{"<script", "alert(1)"},
		},
		{
			name:   "style element is removed",
			in:     `<style>p{color:red}</style><p>x</p>`,
			absent: []string{"<style", "color:red"},
		},
		{
			name:   "event handlers are removed",
			in:     `<p onclick="x()" onmouseover="y()">x</p><img src="cid:a@b" onerror="z()">`,
			absent: []string{"onclick", "onmouseover", "onerror"},
		},
		{
			name:   "style attribute is removed",
			in:     `<p style="background:url(http://t.example/x)">x</p>`,
			absent: []string{"style=", "t.example"},
		},
		{
			name:   "remote image is blocked",
			in:     `<img src="https://tracker.example/p.gif" alt="t">`,
			absent: []string{"tracker.example", "<img src"},
		},
		{
			name:   "protocol-relative and http images are blocked",
			in:     `<img src="//t.example/a.png"><img src="http://t.example/b.png">`,
			absent: []string{"t.example"},
		},
		{
			name:     "cid image is kept",
			in:       `<img src="cid:logo@example.org" alt="logo">`,
			contains: []string{`src="cid:logo@example.org"`, `alt="logo"`},
		},
		{
			name:   "data URI image is blocked",
			in:     `<img src="data:image/png;base64,AAAA">`,
			absent: []string{"data:image"},
		},
		{
			name:     "https link is kept",
			in:       `<a href="https://example.org/x">x</a>`,
			contains: []string{`href="https://example.org/x"`},
		},
		{
			name:   "javascript link is removed",
			in:     `<a href="javascript:alert(1)">x</a>`,
			absent: []string{"javascript:", "href"},
		},
		{
			name:   "data link is removed",
			in:     `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`,
			absent: []string{"data:", "href"},
		},
		{
			name:   "forms, frames and objects are removed",
			in:     `<form action="http://x"><input name="p"></form><iframe src="http://x"></iframe><object data="x"></object>`,
			absent: []string{"<form", "<input", "<iframe", "<object", "http://x"},
		},
		{
			name:     "basic formatting and lists are kept",
			in:       `<ul><li><b>one</b> <em>two</em></li></ul>`,
			contains: []string{"<ul>", "<li>", "<b>one</b>", "<em>two</em>"},
		},
		{
			name:     "empty input",
			in:       ``,
			contains: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeHTML(tc.in)
			for _, s := range tc.contains {
				if !strings.Contains(got, s) {
					t.Errorf("output missing %q\n got: %s", s, got)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("output still contains %q\n got: %s", s, got)
				}
			}
		})
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText(`<p>First &amp; second</p><p>Third<br>line</p><script>x()</script>`)
	want := "First & second\nThird\nline"
	if got != want {
		t.Errorf("htmlToText = %q, want %q", got, want)
	}
}
