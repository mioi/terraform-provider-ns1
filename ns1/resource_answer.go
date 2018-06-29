package ns1

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/schema"

	ns1 "gopkg.in/ns1/ns1-go.v2/rest"
	"gopkg.in/ns1/ns1-go.v2/rest/model/data"
	"gopkg.in/ns1/ns1-go.v2/rest/model/dns"
)

func answerResource() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			// Required
			"record": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"answer": {
				Type:     schema.TypeString,
				Required: true,
			},
			// Optional
			"region": {
				Type:     schema.TypeString,
				Optional: true,
			},
			"meta": {
				Type:     schema.TypeMap,
				Optional: true,
			},
		},
		Create: AnswerCreate,
		Read:   AnswerRead,
		Update: AnswerUpdate,
		Delete: AnswerDelete,
	}
}

func answerToResourceData(d *schema.ResourceData, a *dns.Answer) error {
	m := answerToMap(*a)
	d.Set("answer", m["answer"])
	if a.RegionName != "" {
		d.Set("region", m["region"])
	}
	if a.Meta != nil {
		d.Set("meta", m["meta"])
	}
	d.SetId(answerIDHash(d))
	return nil
}

func resourceDataToAnswer(a *dns.Answer, d *schema.ResourceData, old bool) error {
	var answer string
	var region string
	var meta map[string]interface{}
	oldAnswer, newAnswer := d.GetChange("answer")
	oldRegion, newRegion := d.GetChange("region")
	oldMeta, newMeta := d.GetChange("meta")
	if old {
		answer = oldAnswer.(string)
		region = oldRegion.(string)
		meta = oldMeta.(map[string]interface{})
	} else {
		answer = newAnswer.(string)
		region = newRegion.(string)
		meta = newMeta.(map[string]interface{})
	}
	a.Rdata = strings.Split(answer, " ")
	if region != "" {
		a.RegionName = region
	}
	if meta != nil {
		a.Meta = data.MetaFromMap(meta)
		errs := a.Meta.Validate()
		if len(errs) > 0 {
			return errJoin(append([]error{errors.New("found error/s in answer metadata")}, errs...), ",")
		}
	}
	return nil
}

func findRecord(client *ns1.Client, domain string) (*dns.Record, error) {
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

func findAnswer(d *schema.ResourceData, r *dns.Record, old bool) (*dns.Answer, error) {
	var answer *dns.Answer
	a := d.Get("answer").(string)
	switch r.Type {
	case "TXT", "SPF":
		answer = dns.NewTXTAnswer(a)
	default:
		answer = dns.NewAnswer(strings.Split(a, " "))
	}
	if err := resourceDataToAnswer(answer, d, true); err != nil {
		return nil, err
	}
	for _, a := range r.Answers {
		if a.String() != answer.String() {
			continue
		}
		if a.RegionName != answer.RegionName {
			continue
		}
		if !reflect.DeepEqual(a.Meta.StringMap(), answer.Meta.StringMap()) {
			continue
		}
		return a, nil
	}
	return nil, nil
}

func updateRecord(op string, meta interface{}, d *schema.ResourceData) (*dns.Answer, error) {
	client := meta.(*ns1.Client)
	var a *dns.Answer
	// get the record to get the zone before creating lock
	r, err := findRecord(client, d.Get("record").(string))
	if err != nil {
		return nil, err
	}
	err = RecordMutex.Lock(client, a, d.Get("record").(string), r.Zone)
	if err != nil {
		return nil, err
	}
	log.Printf("[DEBUG] LOCKED RECORD FOR %v", d.Get("answer"))
	defer RecordMutex.Unlock(client, a, d.Get("record").(string), r.Zone)
	// get the record again after creating lock
	r, err = findRecord(client, d.Get("record").(string))
	if err != nil {
		return nil, err
	}
	switch r.Type {
	case "TXT", "SPF":
		a = dns.NewTXTAnswer(d.Get("answer").(string))
	default:
		a = dns.NewAnswer(strings.Split(d.Get("answer").(string), " "))
	}
	if err := resourceDataToAnswer(a, d, false); err != nil {
		return nil, err
	}
	switch op {
	case "create":
		// Create the answer
		r.AddAnswer(a)
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
	case "update":
		old, err := findAnswer(d, r, true)
		if err != nil {
			return nil, err
		}
		// Replace the answer
		for i := 0; i < len(r.Answers); i++ {
			if r.Answers[i] == old {
				r.Answers[i] = a
				break
			}
		}
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
	case "delete":
		old, err := findAnswer(d, r, false)
		if err != nil {
			return nil, err
		}
		// Delete the answer
		for i := len(r.Answers) - 1; i >= 0; i-- {
			if r.Answers[i] == old {
				r.Answers = append(r.Answers[:i], r.Answers[i+1:]...)
			}
		}
		if _, err := client.Records.Update(r); err != nil {
			return nil, err
		}
		d.SetId("")
	}
	log.Printf("[DEBUG] FINISHED UPDATING RECORD FOR %v", d.Get("answer"))
	return a, nil
}

func answerIDHash(d *schema.ResourceData) string {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%s-", d.Get("record").(string)))
	buf.WriteString(fmt.Sprintf("%s-", d.Get("answer").(string)))
	if d.Get("region").(string) != "" {
		buf.WriteString(fmt.Sprintf("%s-", d.Get("region").(string)))
	}
	if d.Get("meta").(map[string]interface{}) != nil {
		for k, v := range d.Get("meta").(map[string]interface{}) {
			buf.WriteString(fmt.Sprintf("%s-%s-", k, v))
		}
	}
	return fmt.Sprintf("ans-%d", hashcode.String(buf.String()))
}

// AnswerCreate creates answer for given record in ns1
func AnswerCreate(d *schema.ResourceData, meta interface{}) error {
	a, err := updateRecord("create", meta, d)
	if err != nil {
		return err
	}
	return answerToResourceData(d, a)
}

// AnswerRead reads the answer for given record from ns1
func AnswerRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ns1.Client)
	r, err := findRecord(client, d.Get("record").(string))
	if err != nil {
		if !strings.Contains(err.Error(), "record not found") {
			return err
		}
	}
	a, err := findAnswer(d, r, false)
	if err != nil {
		return err
	}
	// Could not find answer
	if a == nil {
		d.SetId("")
		return nil
	}
	return answerToResourceData(d, a)
}

// AnswerDelete deletes the answer from the record from ns1
func AnswerDelete(d *schema.ResourceData, meta interface{}) error {
	_, err := updateRecord("delete", meta, d)
	if err != nil {
		return err
	}
	return nil
}

// AnswerUpdate updates the given answer in the record in ns1
func AnswerUpdate(d *schema.ResourceData, meta interface{}) error {
	a, err := updateRecord("update", meta, d)
	if err != nil {
		return err
	}
	return answerToResourceData(d, a)
}
