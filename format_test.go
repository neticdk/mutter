package main

import "testing"

func TestFormatText(t *testing.T) {
	b, i, s, c := boldStyle.Render, italicStyle.Render, strikeStyle.Render, codeStyle.Render
	if b("x") == "x" {
		t.Fatal("styles render without ANSI codes, so the cases below would pass vacuously")
	}

	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"*bold*", b("bold")},
		{"a *b c* d", "a " + b("b c") + " d"},
		{"_it_ and ~gone~", i("it") + " and " + s("gone")},
		{"snake_case_name", "snake_case_name"},
		{"2*3*4", "2*3*4"},
		{"* not bold *", "* not bold *"},
		{"(*x*)", "(" + b("x") + ")"},
		{"`*raw*`", c("*raw*")},
		{"```\nfn *x*\n```", c("fn *x*")},
	}
	for _, tt := range tests {
		if got := formatText(tt.in); got != tt.want {
			t.Errorf("formatText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
