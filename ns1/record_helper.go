package ns1

import (
	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/mutexkv"
	ns1 "gopkg.in/ns1/ns1-go.v2/rest"
	"gopkg.in/ns1/ns1-go.v2/rest/model/dns"
	"log"
	"strconv"
)

type RecordMutexKV struct {
	mutexkv *mutexkv.MutexKV
	tracker map[string][]interface{}
}

// Lock creates a TXT record in the given zone indicating that the given record is being altered, so that parallel
// executions of this provider are synchronized. This is safe for use by multiple goroutines. A non-nil error is
// returned if the TXT record cannot be created.
func (m *RecordMutexKV) Lock(client *ns1.Client, i interface{}, record string, zone string) error {
	log.Printf("[DEBUG] Locking Record %q", record)
	hashRecord := strconv.Itoa(hashcode.String(record)) + "." + zone
	m.mutexkv.Lock(hashRecord)
	m.mutexkv.Lock(record)
	defer m.mutexkv.Unlock(record)

	if recordTracker, ok := m.tracker[record]; ok {
		for p := range recordTracker {
			if p == i {
				return nil
			}
		}
	} else {
		r := dns.NewRecord(zone, hashRecord, "TXT")
		if _, err := client.Records.Create(r); err != nil {
			return err
		}
	}

	m.tracker[record] = append(m.tracker[record], i)

	log.Printf("[DEBUG] Locked Record %q", record)
	return nil
}

// Unlock removes object `i` as a holder of the TXT record lock. If no such holders are left after this, Unlock
// deletes the TXT record. This is safe for use by multiple goroutines. A non-nil error is returned if the TXT record
// cannot be deleted.
func (m *RecordMutexKV) Unlock(client *ns1.Client, i interface{}, record string, zone string) error {
	log.Printf("[DEBUG] Unlocking Record %q", record)
	hashRecord := strconv.Itoa(hashcode.String(record)) + "." + zone
	m.mutexkv.Lock(record)
	defer m.mutexkv.Unlock(record)
	defer m.mutexkv.Unlock(hashRecord)

	if recordTracker, ok := m.tracker[record]; ok {
		for t := len(recordTracker) - 1; t >= 0; t-- {
			if recordTracker[t] == i {
				m.tracker[record] = append(recordTracker[:t], recordTracker[t+1:]...)
			}
		}
	}
	if len(m.tracker[record]) == 0 {
		if _, err := client.Records.Delete(zone, hashRecord, "TXT"); err != nil {
			return err
		}
	}

	log.Printf("[DEBUG] Unlocked Record %q", record)
	return nil
}

// Returns a properly initalized RecordMutexKV
func NewRecordMutexKV() *RecordMutexKV {
	mutexKV := mutexkv.NewMutexKV()
	return &RecordMutexKV{
		mutexkv: mutexKV,
		tracker: make(map[string][]interface{}),
	}
}
