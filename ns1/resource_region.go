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
	}
}

// Get Region from Regions
func getRegion(r data.Regions) (string, *data.Region) {
	var name string
	var region *data.Region
	for k, v := range r {
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

func regionsToResourceData(d *schema.ResourceData, regions data.Regions) error {
	m := regionToMap(regions)
	d.Set("name", m["name"])
	if m["meta"] != nil {
		d.Set("meta", m["meta"])
	}
	d.SetId(regionIDHash(d))
	return nil
}

func resourceDataToRegions(regions data.Regions, d *schema.ResourceData, old bool) error {
	var name string
	var meta map[string]interface{}
	oldName, newName := d.GetChange("name")
	oldMeta, newMeta := d.GetChange("meta")
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
		for _, record := range zone.Records {
			if domain == record.Domain {
				r, _, err := client.Records.Get(z.Zone, record.Domain, record.Type)
				if err != nil {
					return nil, err
				}
				return r, err
			}
		}
	}
	return nil, fmt.Errorf("record not found: %s", domain)
}

func findRegion(d *schema.ResourceData, record *dns.Record, old bool) (data.Regions, error) {
	var regions = data.Regions{}
	if err := resourceDataToRegions(regions, d, true); err != nil {
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

func updateRecordForRegion(op string, meta interface{}, d *schema.ResourceData) (data.Regions, error) {
	client := meta.(*ns1.Client)
	var regions = data.Regions{}
	// get the record to get the zone before creating lock
	r, err := findRecordByRegion(client, d.Get("record").(string))
	if err != nil {
		return nil, err
	}
	err = RecordMutex.Lock(client, &regions, d.Get("record").(string), r.Zone)
	if err != nil {
		return nil, err
	}
	defer RecordMutex.Unlock(client, &regions, d.Get("record").(string), r.Zone)
	// get the record again after creating lock
	r, err = findRecordByRegion(client, d.Get("record").(string))
	if err != nil {
		return nil, err
	}
	if err := resourceDataToRegions(regions, d, false); err != nil {
		return nil, err
	}
	switch op {
	case "create":
		// Create the region
		region := data.Region{
			Meta: data.Meta{},
		}
		meta := data.MetaFromMap(d.Get("meta").(map[string]interface{}))
		region.Meta = *meta
		errs := region.Meta.Validate()
		if len(errs) > 0 {
			return nil, errJoin(append([]error{errors.New("found error/s in region metadata")}, errs...), ",")
		}
		r.Regions[d.Get("name").(string)] = region
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
		regions[d.Get("name").(string)] = region
	case "update":
		_, err := findRegion(d, r, true)
		if err != nil {
			return nil, err
		}
		// Replace the region
		region := r.Regions[d.Get("name").(string)]
		meta := data.MetaFromMap(d.Get("meta").(map[string]interface{}))
		region.Meta = *meta
		errs := region.Meta.Validate()
		if len(errs) > 0 {
			return nil, errJoin(append([]error{errors.New("found error/s in region metadata")}, errs...), ",")
		}
		r.Regions[d.Get("name").(string)] = region
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
		regions[d.Get("name").(string)] = region
	case "delete":
		_, err := findRegion(d, r, false)
		if err != nil {
			return nil, err
		}
		// Delete the region
		delete(r.Regions, d.Get("name").(string))
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
		d.SetId("")
	}
	return regions, nil
}

func regionIDHash(d *schema.ResourceData) string {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%s-", d.Get("record").(string)))
	buf.WriteString(fmt.Sprintf("%s-", d.Get("name").(string)))
	if d.Get("meta").(map[string]interface{}) != nil {
		for k, v := range d.Get("meta").(map[string]interface{}) {
			buf.WriteString(fmt.Sprintf("%s-%s-", k, v))
		}
	}
	return fmt.Sprintf("reg-%d", hashcode.String(buf.String()))
}

// RegionCreate creates region for given record in ns1
func RegionCreate(d *schema.ResourceData, meta interface{}) error {
	regions, err := updateRecordForRegion("create", meta, d)
	if err != nil {
		return err
	}
	return regionsToResourceData(d, regions)
}

// RegionRead reads the region for given record from ns1
func RegionRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ns1.Client)
	r, err := findRecordByRegion(client, d.Get("record").(string))
	if err != nil {
		if !strings.Contains(err.Error(), "record not found") {
			return err
		}
	}
	region, err := findRegion(d, r, false)
	if err != nil {
		return err
	}
	// Could not find region
	if region == nil {
		d.SetId("")
		return nil
	}
	return regionsToResourceData(d, region)
}

// RegionDelete deletes the region from the record from ns1
func RegionDelete(d *schema.ResourceData, meta interface{}) error {
	_, err := updateRecordForRegion("delete", meta, d)
	if err != nil {
		return err
	}
	return nil
}

// RegionUpdate updates the given region in the record in ns1
func RegionUpdate(d *schema.ResourceData, meta interface{}) error {
	region, err := updateRecordForRegion("update", meta, d)
	if err != nil {
		return err
	}
	return regionsToResourceData(d, region)
}
