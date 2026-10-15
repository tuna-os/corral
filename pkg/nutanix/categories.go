package nutanix

// Categories: Nutanix's tags.
//
// Prism Central organizes entities with categories, key:value pairs an
// administrator defines centrally, not free-form labels. Corral's Tagger
// maps onto category association: the tag names an existing key and value,
// Corral resolves it to the category extId with the prism namespace's
// $filter (key eq … and value eq …), and associates or disassociates it on
// the VM through the vmm $actions verbs, with the VM's ETag as If-Match.
// Corral never creates a category: categories drive policy (security,
// protection, placement), so their definition stays with the administrator.
//
// Request shapes follow Nutanix's own v4 sample
// (nutanixdev/code-samples python/v4api_sdk/assign_categories_to_vms_requests.py):
// GET /prism/{ver}/config/categories?$filter=key eq 'k' and value eq 'v', and
// POST /vmm/{ver}/ahv/config/vms/{extId}/$actions/associate-categories with
// {"categories": [{"extId": …}]}.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ParseCategory splits a Corral tag into a category key and value. Both
// "key=value" and "key:value" are accepted; a bare word is refused because
// a category always has both halves.
func ParseCategory(tag string) (key, value string, err error) {
	i := strings.IndexAny(tag, "=:")
	if i > 0 && i < len(tag)-1 {
		key, value = strings.TrimSpace(tag[:i]), strings.TrimSpace(tag[i+1:])
	}
	if key == "" || value == "" {
		return "", "", fmt.Errorf("nutanix: tag %q is not a category; Nutanix tags are categories, so use key=value (e.g. env=prod)", tag)
	}
	return key, value, nil
}

// categoryList is the prism categories list envelope.
type categoryList struct {
	Data []struct {
		ExtID string `json:"extId"`
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"data"`
}

// CategoryExtID resolves key:value to the category's extId.
func (c *Client) CategoryExtID(key, value string) (string, error) {
	q := url.Values{}
	q.Set("$filter", "key eq '"+odataEscape(key)+"' and value eq '"+odataEscape(value)+"'")
	var resp categoryList
	if err := c.get("/prism/"+c.cfg.APIVersion+"/config/categories?"+q.Encode(), &resp); err != nil {
		return "", err
	}
	for _, cat := range resp.Data {
		if cat.Key == key && cat.Value == value && cat.ExtID != "" {
			return cat.ExtID, nil
		}
	}
	return "", fmt.Errorf("nutanix: category %s:%s does not exist; an administrator defines categories in Prism Central, and Corral only assigns them", key, value)
}

// SetCategory associates (on) or disassociates (!on) the category a tag
// names. A VM that already has, or already lacks, the category returns an
// invalid Task and no request is made: the end state is already true.
func (c *Client) SetCategory(name, tag string, on bool) (Task, error) {
	key, value, err := ParseCategory(tag)
	if err != nil {
		return Task{}, err
	}
	vm, err := c.Resolve(name)
	if err != nil {
		return Task{}, err
	}
	catID, err := c.CategoryExtID(key, value)
	if err != nil {
		return Task{}, err
	}
	has := false
	for _, ref := range vm.Categories {
		if ref.ExtID == catID {
			has = true
		}
	}
	if has == on {
		return Task{}, nil
	}
	verb := "associate-categories"
	if !on {
		verb = "disassociate-categories"
	}
	path := c.vmmPath("/" + vm.ExtID)
	etag, err := c.getETag(path)
	if err != nil {
		return Task{}, err
	}
	body := map[string]any{"categories": []map[string]string{{"extId": catID}}}
	var resp actionResponse
	if err := c.doWithHeaders(http.MethodPost, path+"/$actions/"+verb, body, &resp,
		map[string]string{"If-Match": etag}); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: %s %s: mutation returned no task extId", verb, name)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}
