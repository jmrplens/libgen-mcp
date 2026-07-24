package libgen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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

// DetailsByMD5 returns the file record and its first related edition.
func (c *Client) DetailsByMD5(ctx context.Context, md5 string) (file, edition map[string]any, err error) {
	body, _, err := c.get(ctx, "/json.php", url.Values{"object": {"f"}, "md5": {md5}, "addkeys": {"*"}})
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

// DetailsByID returns a record by id. object: "e" (edition) or "f" (file).
func (c *Client) DetailsByID(ctx context.Context, object, id string) (map[string]any, error) {
	if object != "e" && object != "f" {
		return nil, fmt.Errorf("object must be \"e\" or \"f\", got %q", object)
	}
	q := url.Values{"object": {object}, "ids": {id}}
	if object == "f" {
		q.Set("addkeys", "*")
	}
	body, _, err := c.get(ctx, "/json.php", q)
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
