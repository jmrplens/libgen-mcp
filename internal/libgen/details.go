package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
)

// decodeObjects interprets the json.php response (map of id → object).
// A `[]` response (empty array) means "no results".
func decodeObjects(body []byte) (map[string]map[string]any, error) {
	var objs map[string]map[string]any
	if err := json.Unmarshal(body, &objs); err != nil {
		var empty []any
		if json.Unmarshal(body, &empty) == nil && len(empty) == 0 {
			return map[string]map[string]any{}, nil
		}
		return nil, fmt.Errorf("unexpected json.php response: %w", err)
	}
	return objs, nil
}

// jsonAPIPath is the catalog's JSON endpoint, used by every lookup here.
const jsonAPIPath = "/json.php"

// DetailsByMD5 returns the file record and its first related edition.
func (c *Client) DetailsByMD5(ctx context.Context, md5 string) (file, edition map[string]any, err error) {
	body, _, err := c.get(ctx, jsonAPIPath, url.Values{"object": {"f"}, "md5": {md5}, "addkeys": {"*"}})
	if err != nil {
		return nil, nil, err
	}
	files, err := decodeObjects(body)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		// A search that escalated to the extra sources returns md5s the catalog
		// never had, so telling the caller to run a search first would send it back
		// down a path it already took. Name the catalog instead, so a caller that
		// has a fallback index knows which one came up empty.
		return nil, nil, fmt.Errorf("the Library Genesis catalog has no record for md5 %s", md5)
	}
	for id, f := range files {
		f["file_id"] = id
		file = f
		break
	}
	if eds, ok := file["editions"].(map[string]any); ok {
		for _, e := range eds {
			em, isMap := e.(map[string]any)
			if !isMap {
				continue
			}
			if eid, _ := em["e_id"].(string); eid != "" {
				edition, _ = c.DetailsByID(ctx, "e", eid) // best-effort
				break
			}
		}
	}
	return file, edition, nil
}

// DetailsByDOI returns the edition record the catalog holds for a DOI, plus the
// first file that edition points at, so a caller has an md5 to download without a
// second round trip.
//
// It uses json.php's own doi key, which is an exact lookup. Searching for the DOI
// as free text is not a substitute: the catalog's text search matches it loosely
// and returns unrelated articles, including ones that merely carry a different DOI
// in their title.
func (c *Client) DetailsByDOI(ctx context.Context, doi string) (edition, file map[string]any, err error) {
	body, _, err := c.get(ctx, jsonAPIPath, url.Values{"object": {"e"}, "doi": {doi}, "addkeys": {"*"}})
	if err != nil {
		return nil, nil, err
	}
	objs, err := decodeObjects(body)
	if err != nil {
		return nil, nil, err
	}
	if len(objs) == 0 {
		return nil, nil, fmt.Errorf("the Library Genesis catalog has no record for DOI %s", doi)
	}
	// Go randomizes map iteration, so picking "the first" entry would vary between
	// identical calls. Order by id so a DOI that resolves to more than one edition
	// answers the same way every time.
	ids := slices.Sorted(maps.Keys(objs))
	edition = objs[ids[0]]
	edition["edition_id"] = ids[0]
	return edition, firstEditionFile(edition), nil
}

// firstEditionFile returns the first file an edition record points at, or nil when
// it points at none. The file is what carries the md5 a download needs.
func firstEditionFile(edition map[string]any) map[string]any {
	files, ok := edition["files"].(map[string]any)
	if !ok {
		return nil
	}
	// Ordered for the same reason as the edition: an edition with several files must
	// hand back the same md5 on every call.
	for _, id := range slices.Sorted(maps.Keys(files)) {
		if fm, isMap := files[id].(map[string]any); isMap {
			return fm
		}
	}
	return nil
}

// DetailsByID returns a record by id. object: "e" (edition) or "f" (file).
func (c *Client) DetailsByID(ctx context.Context, object, id string) (map[string]any, error) {
	if object != "e" && object != "f" {
		return nil, fmt.Errorf("object must be \"e\" or \"f\", got %q", object)
	}
	q := url.Values{"object": {object}, "ids": {id}}
	if object == "f" {
		q.Set("addkeys", "*")
	}
	body, _, err := c.get(ctx, jsonAPIPath, q)
	if err != nil {
		return nil, err
	}
	objs, err := decodeObjects(body)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("no %s record found with id %s — verify the id comes from a search result or get_details response", object, id)
	}
	for oid, o := range objs {
		o["id"] = oid
		return o, nil
	}
	return nil, fmt.Errorf("no %s record found with id %s — verify the id comes from a search result or get_details response", object, id)
}
