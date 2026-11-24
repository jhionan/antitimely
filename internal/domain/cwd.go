package domain

import (
	"path"
	"strings"
)

// windowsCwd returns a slash-separated, case-folded Win32 path. WSL share
// names are case-insensitive, but directories inside the distro are not.
func windowsCwd(raw string) (string, bool) {
	s := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(s, "//?/") {
		s = s[4:]
		if len(s) >= 4 && strings.EqualFold(s[:4], "unc/") {
			s = "//" + s[4:]
		}
	}
	if driveMount(s) {
		s = s[5:6] + ":" + s[6:]
		if len(s) == 2 {
			s += "/"
		}
	}
	if len(s) >= 3 && asciiLetter(s[0]) && s[1:3] == ":/" {
		return strings.ToLower(s[:2] + path.Clean(s[2:])), true
	}
	if !strings.HasPrefix(s, "//") {
		return raw, false
	}
	host, rest, ok := strings.Cut(s[2:], "/")
	share, rest, _ := strings.Cut(rest, "/")
	if !ok || host == "" || share == "" || host == "." || share == "." || share == ".." {
		return raw, false
	}
	host, share = strings.ToLower(host), strings.ToLower(share)
	tail := path.Clean("/" + rest)
	if host == "wsl$" || host == "wsl.localhost" {
		if driveMount(tail) {
			return windowsCwd(tail)
		}
		host = "wsl.localhost"
	} else {
		tail = strings.ToLower(tail)
	}
	return "//" + host + "/" + share + strings.TrimSuffix(tail, "/"), true
}

func asciiLetter(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

func driveMount(s string) bool {
	return len(s) >= 6 && strings.HasPrefix(s, "/mnt/") && asciiLetter(s[5]) &&
		(len(s) == 6 || s[6] == '/')
}

// TranscriptCwd identifies a WSL transcript's distro from its configured
// location. A Linux path from any other location retains its original meaning.
func TranscriptCwd(cwd, transcriptPath string) string {
	const wsl = "//wsl.localhost/"
	if !strings.HasPrefix(cwd, "/") || strings.HasPrefix(cwd, "//") || driveMount(cwd) {
		return cwd
	}
	source, _ := windowsCwd(transcriptPath)
	if !strings.HasPrefix(source, wsl) {
		return cwd
	}
	distro, _, _ := strings.Cut(source[len(wsl):], "/")
	return wsl + distro + path.Clean(cwd)
}
