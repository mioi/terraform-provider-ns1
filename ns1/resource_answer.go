package ns1

import (
	"bytes"
	"errors"
	"fmt"
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
		Importer: &schema.ResourceImporter{State: AnswerStateFunc},
	}
}

func answerToResourceData(resourceData *schema.ResourceData, a *dns.Answer) error {
	m := answerToMap(*a)
	resourceData.Set("answer", m["answer"])
	if a.RegionName != "" {
		resourceData.Set("region", m["region"])
	}
	if a.Meta != nil {
		resourceData.Set("meta", m["meta"])
	}
	resourceData.SetId(answerIDHash(resourceData))
	return nil
}

func resourceDataToAnswer(a *dns.Answer, resourceData *schema.ResourceData, old bool) error {
	var answer string
	var region string
	var meta map[string]interface{}
	oldAnswer, newAnswer := resourceData.GetChange("answer")
	oldRegion, newRegion := resourceData.GetChange("region")
	oldMeta, newMeta := resourceData.GetChange("meta")
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

func findRecordByAnswer(client *ns1.Client, domain string) (*dns.Record, error) {
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

func findAnswer(resourceData *schema.ResourceData, record *dns.Record, old bool) (*dns.Answer, error) {
	var answer *dns.Answer
	a := resourceData.Get("answer").(string)
	switch record.Type {
	case "TXT", "SPF":
		answer = dns.NewTXTAnswer(a)
	default:
		answer = dns.NewAnswer(strings.Split(a, " "))
	}
	if err := resourceDataToAnswer(answer, resourceData, true); err != nil {
		return nil, err
	}
	for _, a := range record.Answers {
		if a.String() != answer.String() {
			continue
		}
		// short-circuit if we only have the name of the answer
		if answer.RegionName == "" && len(answer.Meta.StringMap()) == 0 {
			return a, nil
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

func updateRecordForAnswer(op string, meta interface{}, resourceData *schema.ResourceData) (*dns.Answer, error) {
	client := meta.(*ns1.Client)
	var answer *dns.Answer
	// get the record to get the zone before creating lock
	record, err := findRecordByAnswer(client, resourceData.Get("record").(string))
	if err != nil {
		return nil, err
	}
	err = RecordMutex.Lock(client, answer, resourceData.Get("record").(string), record.Zone)
	if err != nil {
		return nil, err
	}
	defer RecordMutex.Unlock(client, answer, resourceData.Get("record").(string), record.Zone)
	// get the record again after creating lock
	record, err = findRecordByAnswer(client, resourceData.Get("record").(string))
	if err != nil {
		return nil, err
	}
	switch record.Type {
	case "TXT", "SPF":
		answer = dns.NewTXTAnswer(resourceData.Get("answer").(string))
	default:
		answer = dns.NewAnswer(strings.Split(resourceData.Get("answer").(string), " "))
	}
	if err := resourceDataToAnswer(answer, resourceData, false); err != nil {
		return nil, err
	}
	switch op {
	case "create":
		// Create the answer
		record.AddAnswer(answer)
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
	case "update":
		old, err := findAnswer(resourceData, record, true)
		if err != nil {
			return nil, err
		}
		// Replace the answer
		for i := 0; i < len(record.Answers); i++ {
			if record.Answers[i] == old {
				record.Answers[i] = answer
				break
			}
		}
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
	case "delete":
		old, err := findAnswer(resourceData, record, false)
		if err != nil {
			return nil, err
		}
		// Delete the answer
		for i := len(record.Answers) - 1; i >= 0; i-- {
			if record.Answers[i] == old {
				record.Answers = append(record.Answers[:i], record.Answers[i+1:]...)
			}
		}
		if _, err := client.Records.Update(record); err != nil {
			return nil, err
		}
		resourceData.SetId("")
	}
	return answer, nil
}

func answerIDHash(resourceData *schema.ResourceData) string {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("%s-", resourceData.Get("record").(string)))
	buf.WriteString(fmt.Sprintf("%s-", resourceData.Get("answer").(string)))
	if resourceData.Get("region").(string) != "" {
		buf.WriteString(fmt.Sprintf("%s-", resourceData.Get("region").(string)))
	}
	if resourceData.Get("meta").(map[string]interface{}) != nil {
		for k, v := range resourceData.Get("meta").(map[string]interface{}) {
			buf.WriteString(fmt.Sprintf("%s-%s-", k, v))
		}
	}
	return fmt.Sprintf("ans-%d", hashcode.String(buf.String()))
}

// AnswerCreate creates answer for given record in ns1
func AnswerCreate(resourceData *schema.ResourceData, meta interface{}) error {
	a, err := updateRecordForAnswer("create", meta, resourceData)
	if err != nil {
		return err
	}
	return answerToResourceData(resourceData, a)
}

// AnswerRead reads the answer for given record from ns1
func AnswerRead(resourceData *schema.ResourceData, meta interface{}) error {
	client := meta.(*ns1.Client)
	r, err := findRecordByAnswer(client, resourceData.Get("record").(string))
	if err != nil {
		if !strings.Contains(err.Error(), "record not found") {
			return err
		}
	}
	a, err := findAnswer(resourceData, r, false)
	if err != nil {
		return err
	}
	// Could not find answer
	if a == nil {
		resourceData.SetId("")
		return nil
	}
	return answerToResourceData(resourceData, a)
}

// AnswerDelete deletes the answer from the record from ns1
func AnswerDelete(resourceData *schema.ResourceData, meta interface{}) error {
	_, err := updateRecordForAnswer("delete", meta, resourceData)
	if err != nil {
		return err
	}
	return nil
}

// AnswerUpdate updates the given answer in the record in ns1
func AnswerUpdate(resourceData *schema.ResourceData, meta interface{}) error {
	a, err := updateRecordForAnswer("update", meta, resourceData)
	if err != nil {
		return err
	}
	return answerToResourceData(resourceData, a)
}


func AnswerStateFunc(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("Invalid answer specifier.  Expecting 1 slash (\"record/answer\"), got %d.", len(parts)-1)
	}

	d.Set("record", parts[0])
	d.Set("answer", parts[1])

	return []*schema.ResourceData{d}, nil
}
