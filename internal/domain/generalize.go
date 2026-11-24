package domain

import (
	"path"
	"strings"
)

// ProposeRule constructs a draft rule from a single observation, using the
// project name as the discriminating attribute when possible.
//
// For agent observations: matches on BinaryName + a cwd prefix containing the
// project name. If the project name doesn't appear in the cwd, falls back to
// the parent directory of cwd.
//
// For focus observations: matches on BundleID + a title substring equal to the
// project name. If the project name doesn't appear in the title, falls back to
// the full title verbatim (caller is expected to surface a notice in this case).
func ProposeRule(obs Observation, projectName string) ProposedRule {
	r := ProposedRule{Priority: 100}

	switch obs.Source {
	case SourceAgent:
		bn := obs.BinaryName
		r.MatchBinaryName = &bn
		r.MatchCwdPrefix = strPtr(generalizeCwd(obs.Cwd, projectName))
	case SourceFocus:
		bid := obs.BundleID
		r.MatchBundleID = &bid
		r.MatchTitleSubstr = strPtr(generalizeTitle(obs.WindowTitle, projectName))
	}

	return r
}

// generalizeCwd finds the longest path prefix of cwd that ends in projectName,
// returning it with a trailing slash. If projectName isn't in cwd, returns
// cwd's parent directory (with trailing slash).
func generalizeCwd(cwd, projectName string) string {
	cleanCwd, isWindows := windowsCwd(cwd)
	root := ""
	if isWindows {
		if strings.HasPrefix(cleanCwd, "//") {
			parts := strings.SplitN(cleanCwd[2:], "/", 3)
			root = "//" + parts[0] + "/" + parts[1]
			cleanCwd = strings.TrimPrefix(cleanCwd, root)
		} else {
			root, cleanCwd = cleanCwd[:2], cleanCwd[2:]
		}
	} else {
		cleanCwd = path.Clean(cwd)
	}
	parts := strings.Split(cleanCwd, "/")

	for i := len(parts) - 1; i >= 0; i-- {
		// Case-insensitive match so e.g. project "Antitimely" finds dir "antitimely".
		if strings.EqualFold(parts[i], projectName) {
			prefix := strings.Join(parts[:i+1], "/")
			return root + prefix + "/"
		}
	}

	// Fallback: parent directory.
	if cleanCwd == "" {
		return root + "/"
	}
	return root + strings.TrimRight(path.Dir(cleanCwd), "/") + "/"
}

// generalizeTitle returns projectName if it appears as a substring of title
// (case-sensitive — matches the daemon's matching), otherwise the full title.
func generalizeTitle(title, projectName string) string {
	if strings.Contains(title, projectName) {
		return projectName
	}
	return title
}

func strPtr(s string) *string { return &s }
