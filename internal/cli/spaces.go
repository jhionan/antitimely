package cli

import (
	"fmt"

	"github.com/rian/antitimely/internal/herdr"
)

// cmdSpaces lists herdr workspaces so their ids are discoverable for
// `rules add --space`. It reads herdr's state directly; no daemon needed.
func cmdSpaces() int {
	r := herdr.NewDefaultResolver()
	spaces := r.Spaces()
	if len(spaces) == 0 {
		fmt.Println("(no herdr spaces found)")
		return 0
	}
	w := len("ID")
	for _, s := range spaces {
		w = max(w, len(s.ID))
	}
	fmt.Printf("%-*s %s\n", w, "ID", "NAME")
	for _, s := range spaces {
		name := s.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%-*s %s\n", w, s.ID, name)
	}
	return 0
}

// spaceLabel renders a herdr workspace id for display as "name (id)", falling
// back to the bare id when herdr no longer knows the space or it is unnamed.
// r may be nil (no herdr state available), in which case the id is shown
// as-is: an id is always more useful than dropping the column.
func spaceLabel(r *herdr.Resolver, id string) string {
	if id == "" {
		return ""
	}
	if r != nil {
		if s, ok := r.Space(id); ok && s.Name != "" {
			return fmt.Sprintf("%s (%s)", s.Name, id)
		}
	}
	return id
}
