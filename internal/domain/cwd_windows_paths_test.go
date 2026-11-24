package domain

import "testing"

func TestMatchWindowsCwd(t *testing.T) {
	tests := []struct {
		pattern, cwd string
		match        bool
	}{
		{`C:\Work\Project\`, `c:/work/PROJECT/src`, true},
		{`C:\Work\Project`, `C:\Work\Project2`, false},
		{`C:\Work\Project`, `D:\Work\Project`, false},
		{`C:\`, `c:\Projects`, true},
		{`C:\Work\[Project]*`, `\\?\c:\work\[project]-2\src`, true},
		{`C:\work`, `/mnt/c/WORK/project`, true},
		{`\\NAS\Share\Project`, `\\?\UNC\nas\share\PROJECT\src`, true},
		{`\\wsl$\Ubuntu\home\Rian\app`, `\\wsl.localhost\ubuntu\home\Rian\app\src`, true},
		{`\\wsl$\Ubuntu\home\Rian\app`, `\\wsl.localhost\ubuntu\home\rian\app\src`, false},
		{`\\wsl$\Ubuntu\home\Rian\app`, `/home/Rian/app/src`, false},
		{`\\wsl.localhost\Ubuntu\mnt\c\Work`, `C:\work\Project`, true},
		{`C:\Work`, `C:Work\Project`, false},
		{`C:\Work`, `C:\Work\..\outside`, false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" vs "+tt.cwd, func(t *testing.T) {
			if err := ValidateCwdPattern(tt.pattern); err != nil {
				t.Fatal(err)
			}
			if got := MatchesCwd(tt.pattern, tt.cwd); got != tt.match {
				t.Fatalf("MatchesCwd = %v, want %v", got, tt.match)
			}
		})
	}
}

func TestTranscriptCwdInWSLDistro(t *testing.T) {
	const root = `\\wsl.localhost\Ubuntu\home\rian\.codex\sessions`
	cwd := TranscriptCwd("/home/Rian/app", root)
	if !MatchesCwd(`\\wsl$\ubuntu\home\Rian\app`, cwd) {
		t.Fatalf("wrong WSL attribution: %q", cwd)
	}
	if MatchesCwd(`\\wsl$\debian\home\Rian\app`, cwd) {
		t.Fatalf("cross-distro attribution: %q", cwd)
	}
}
