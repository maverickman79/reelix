package jellyfin

import "strings"

// imageTypes is the set of image types the reference server accepts, keyed by
// its lowercased spelling so a lookup folds case.
//
// Established by probing a real 10.11.8 rather than from memory: each
// candidate was requested with a well-formed but nonexistent item id, and a
// type the server does not know answers 400 with a validation body naming it
// ("The value 'Poster' is not valid.") where a type it does know falls
// through to the id and answers 404. Poster, Cover and Fanart are the
// controls — all three are rejected. The thirteen below are accepted, which
// is also what the published OpenAPI spec's ImageType enum lists.
var imageTypes = map[string]string{
	"primary":    "Primary",
	"art":        "Art",
	"backdrop":   "Backdrop",
	"banner":     "Banner",
	"logo":       "Logo",
	"thumb":      "Thumb",
	"disc":       "Disc",
	"box":        "Box",
	"screenshot": "Screenshot",
	"menu":       "Menu",
	"chapter":    "Chapter",
	"boxrear":    "BoxRear",
	"profile":    "Profile",
}

// canonicalImageType folds a client's spelling of an image type onto the
// canonical one, reporting whether it is a type at all.
//
// This exists because the image type is a route PARAMETER, and the fold trie
// in routefold.go rewrites literal segments only — deliberately, since
// lowercasing a parameter would corrupt item ids and container extensions. So
// a client that lowercases its paths, as VidHub does, delivers "primary" here
// and nothing upstream will have capitalised it.
//
// That is harmless while Reelix has no artwork and every request 404s. It
// stops being harmless the moment an image exists: a lookup keyed on the
// canonical spelling would miss, and the client would get a 404 for an image
// the server holds. Whoever implements artwork must look up through this
// function rather than through the raw path value.
//
// An empty type means the request omitted it; the reference treats that as
// Primary and so does the caller.
func canonicalImageType(kind string) (string, bool) {
	canonical, ok := imageTypes[strings.ToLower(kind)]
	return canonical, ok
}
