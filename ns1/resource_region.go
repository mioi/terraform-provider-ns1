package ns1

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/schema"

	ns1 "gopkg.in/ns1/ns1-go.v2/rest"
	"gopkg.in/ns1/ns1-go.v2/rest/model/data"
	"gopkg.in/ns1/ns1-go.v2/rest/model/dns"
)

func regionResource() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			// Required
			"record": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			// Optional
			"meta": {
				Type:     schema.TypeMap,
				Optional: true,
			},
		},
		Create: RegionCreate,
		Read:   RegionRead,
		Update: RegionUpdate,
		Delete: RegionDelete,
		Importer: &schema.ResourceImporter{State: RegionStateFunc},
	}
}

// Get Region from Regions
func getRegion(regions data.Regions) (string, *data.Region) {
	var name string
	var region *data.Region
	for k, v := range regions {
		name = k
		region = &v
		break
	}
	return name, region
}

func regionToMap(regions data.Regions) map[string]interface{} {
	m := make(map[string]interface{})
	name, region := getRegion(regions)
	m["name"] = name
	if region != nil {
		m["meta"] = region.Meta.StringMap()
	}
	return m
}

func regionsToResourceData(resourceData *schema.ResourceData, regions data.Regions) error {
	m := regionToMap(regions)
	resourceData.Set("name", m["name"])
	if m["meta"] != nil {
		resourceData.Set("meta", m["meta"])
	}
	resourceData.SetId(regionIDHash(resourceData))
	return nil
}

func resourceDataToRegions(regions data.Regions, resourceData *schema.ResourceData, old bool) error {
	var name string
	var meta map[string]interface{}
	oldName, newName := resourceData.GetChange("name")
	oldMeta, newMeta := resourceData.GetChange("meta")
	if old {
		name = oldName.(string)
		meta = oldMeta.(map[string]interface{})
	} else {
		name = newName.(string)
		meta = newMeta.(map[string]interface{})
	}
	var region data.Region
	if meta != nil {
		region.Meta = *data.MetaFromMap(meta)
		errs := region.Meta.Validate()
		if len(errs) > 0 {
			return errJoin(append([]error{errors.New("found error/s in region metadata")}, errs...), ",")
		}
	}
	regions[name] = region
	return nil
}

func findRecordByRegion(client *ns1.Client, domain string) (*dns.Record, error) {
	zones, _, err := client.Zones.List()
	if err != nil {
		return nil, err
	}
	for _, z := range zones {
		zone, _, err := client.Zones.Get(z.Zone)
		if err != nil {
			return nil, err
		}
		for _, r := range zone.Records {
			if domain == r.Domain {
				record, _, err := client.Records.Get(z.Zone, r.Domain, r.Type)
				if err != nil {
					return nil, err
				}
				return record, err
			}
		}
	}
	return nil, fmt.Errorf("record not found: %s", domain)
}

func findRegion(resourceData *schema.ResourceData, record *dns.Record, old bool) (data.Regions, error) {
	var regions = data.Regions{}
	if err := resourceDataToRegions(regions, resourceData, true); err != nil {
		return nil, err
	}
	name, _ := getRegion(regions)
	for k, v := range record.Regions {
		if k != name {
			continue
		}
		regions[k] = v
		return regions, nil
	}
	return nil, nil
}

func updateRecordForRegion(op string, meta interface{}, resourceData *schema.ResourceData) (data.Regions, error) {
	client := meta.(*ns1.Client)
	var regions = data.Regions{}
	// get the record to get the zone before creating lock
	record, err := findRecordByRegion(client, resourceData.Get("record").(string))
	if err != nil {
		return nil, err
	}
	err = RecordMutex.Lock(client, &regions, resourceData.Get("record").(string), record.Zone)
	if err != nil {
		return nil, err
	}
	defer RecordMutex.Unlock(client, &regions, resourceData.Get("record").(string), record.Zone)
	// get the record again after creating lock
	record, err = findRecordByRegion(client, resourceData.Get("record").(string))
	if err != nil {
		return nil, err
	}
	if err := resourceDataToRegions(regions, resourceData, false); err != nil {
		return nil, err
	}
	switch op {
	case "create":
		// Create the region
		region := data.Region{
			Meta: data.Meta{},
		}
		meta := data.MetaFromMap(resourceData.Get("meta").(map[string]interface{}))
		if meta == nil {
			return nil, errors.New("could not read metadata")
		}
		region.Meta = *meta
		errs := region.Meta.Validate()
		if len(errs) > 0 {
			return nil, errJoin(append([]error{errors.New("found error/s in region metadata")}, errs...), ",")
		}
		record.Regions[resourceData.Get("name").(string)] = region
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
		regions[resourceData.Get("name").(string)] = region
	case "update":
		_, err := findRegion(resourceData, record, true)
		if err != nil {
			return nil, err
		}
		// Replace the region
		region := record.Regions[resourceData.Get("name").(string)]
		meta := data.MetaFromMap(resourceData.Get("meta").(map[string]interface{}))
		if meta == nil {
			return nil, errors.New("could not read metadata")
		}
		region.Meta = *meta
		errs := region.Meta.Validate()
		if len(errs) > 0 {
			return nil, errJoin(append([]error{errors.New("found error/s in region metadata")}, errs...), ",")
		}
		record.Regions[resourceData.Get("name").(string)] = region
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
		regions[resourceData.Get("name").(string)] = region
	case "delete":
		_, err := findRegion(resourceData, record, false)
		if err != nil {
			return nil, err
		}
		// Delete the region
		delete(record.Regions, resourceData.Get("name").(string))
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
		resourceData.SetId("")
	}
	return regions, nil
}

func regionIDHash(resourceData *schema.ResourceData) string {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%s-", resourceData.Get("record").(string)))
	buf.WriteString(fmt.Sprintf("%s-", resourceData.Get("name").(string)))
	if resourceData.Get("meta").(map[string]interface{}) != nil {
		for k, v := range resourceData.Get("meta").(map[string]interface{}) {
			buf.WriteString(fmt.Sprintf("%s-%s-", k, v))
		}
	}
	return fmt.Sprintf("reg-%d", hashcode.String(buf.String()))
}

// RegionCreate creates region for given record in ns1
func RegionCreate(resourceData *schema.ResourceData, meta interface{}) error {
	regions, err := updateRecordForRegion("create", meta, resourceData)
	if err != nil {
		return err
	}
	return regionsToResourceData(resourceData, regions)
}

// RegionRead reads the region for given record from ns1
func RegionRead(resourceData *schema.ResourceData, meta interface{}) error {
	client := meta.(*ns1.Client)
	record, err := findRecordByRegion(client, resourceData.Get("record").(string))
	if err != nil {
		if !strings.Contains(err.Error(), "record not found") {
			return err
		}
	}
	region, err := findRegion(resourceData, record, false)
	if err != nil {
		return err
	}
	// Could not find region
	if region == nil {
		resourceData.SetId("")
		return nil
	}
	return regionsToResourceData(resourceData, region)
}

// RegionDelete deletes the region from the record from ns1
func RegionDelete(resourceData *schema.ResourceData, meta interface{}) error {
	_, err := updateRecordForRegion("delete", meta, resourceData)
	if err != nil {
		return err
	}
	return nil
}

// RegionUpdate updates the given region in the record in ns1
func RegionUpdate(resourceData *schema.ResourceData, meta interface{}) error {
	region, err := updateRecordForRegion("update", meta, resourceData)
	if err != nil {
		return err
	}
	return regionsToResourceData(resourceData, region)
}

func RegionStateFunc(resourceData *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(resourceData.Id(), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("Invalid region specifier.  Expecting 1 slash (\"record/name\"), got %d.", len(parts)-1)
	}

	resourceData.Set("record", parts[0])
	resourceData.Set("name", parts[1])

	return []*schema.ResourceData{resourceData}, nil
}