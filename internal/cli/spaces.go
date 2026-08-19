package cli

import (
	"fmt"

	"github.com/rian/antitimely/internal/herdr"
)

// cmdSpaces lists herdr workspaces so their ids are discoverable for
// `rules add --space`. It reads herdr's state directly; no daemon needed.
func cmdSpaces() int {
	r := herdr.NewResolver(herdr.DefaultSessionPath())
	spaces := r.Spaces()
	if len(spaces) == 0 {
		fmt.Println("(no herdr spaces found)")
		return 0
	}
	fmt.Printf("%-6s %s\n", "ID", "NAME")
	for _, s := range spaces {
		name := s.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%-6s %s\n", s.ID, name)
	}
	return 0
}
