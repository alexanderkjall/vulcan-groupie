package db

import (
	"bytes"
	"crypto/md5"
	"encoding/gob"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sync"

	"github.com/adevinta/vulcan-groupie/pkg/models"
	report "github.com/adevinta/vulcan-report"
)

type entry struct {
	ScanID string
	Date   string
	Report report.Report
}

// MemDB is a concurrent-safe persistence layer stored in memory.
// It stores both the vulnerabilities detected for every scan.
type MemDB struct {
	// Historic is a map that stores the reports generated by the
	// execution of every Check.
	// The map key is a MD5 sum of the concatenation of the following fields:
	// - Name of the Checktype for the executed Check
	// - Target of the executed Check
	// - Options of the executed Check
	// Those three fields together conform the PRIMARY KEY.
	// The value of map entries is an array of struct containing:
	// - The scanID the Check pertains to
	// - The date of the scan when the Check was executed
	// - The Vulcan Core Report associated to it.
	// The array is ordered by execution time, so last entry overwrites previous one.
	Historic map[string][]entry

	mux *sync.RWMutex
}

// NewMemDB creates a new MemDB instance and returns its address.
func NewMemDB() *MemDB {
	return &MemDB{
		Historic: make(map[string][]entry),
		mux:      &sync.RWMutex{},
	}
}

// SaveScanVulnerabilities gets a list of Vulcan Core Reports and updates the historic.
func (m *MemDB) SaveScanVulnerabilities(scanID string, date string, reports []report.Report) error {
	m.mux.Lock()
	defer m.mux.Unlock()

	for _, r := range reports {
		if r.Status != "FINISHED" {
			continue
		}

		e := entry{
			ScanID: scanID,
			Date:   date,
			Report: r,
		}

		h := md5.New()
		io.WriteString(h, r.ChecktypeName)
		io.WriteString(h, r.Target)
		io.WriteString(h, r.Options)
		key := fmt.Sprintf("%x", h.Sum(nil))

		m.Historic[key] = append(m.Historic[key], e)
	}

	return nil
}

// GetScanVulnerabilities retrieves the vulnerabilities of the given scans
// from the storage.
func (m *MemDB) GetScanVulnerabilities(scanID ...string) ([]models.Vulnerability, error) {
	m.mux.RLock()
	historic := m.Historic
	m.mux.RUnlock()

	// First we iterate by all the reports stored in the historic table.
	// The goal is to find whether a vulnerability has been detected for more than
	// one Target and group them in that way. Also we need to take into account that
	// vulnerabilities with the same Summary but different Severity shouldn't be grouped.
	// To do that, we are using a map where the key is the summary of the vulnerability
	// and the value is another map. The key for the inner map is the SeverityRank, and
	// the value is the vulnerability + the list of the affected targets.
	vmap := make(map[string]map[report.SeverityRank]models.Vulnerability)
	for _, entriesArray := range historic {
		// We traverse the historic array values in inverse order, because the last entries
		// are more actual than the previous ones.
		for i := len(entriesArray) - 1; i >= 0; i-- {
			for _, s := range scanID {
				if entriesArray[i].ScanID != s {
					continue
				}

				for _, vuln := range entriesArray[i].Report.Vulnerabilities {
					// NOTE: in the future when using the ID field of the Vulnerabilities, change by:
					// mm, ok := vmap[vuln.ID]
					mm, ok := vmap[vuln.Summary]
					if !ok {
						mm = make(map[report.SeverityRank]models.Vulnerability)
					}

					mv, ok := mm[vuln.Severity()]
					if !ok {
						mv = models.Vulnerability{
							Vulnerability: vuln,
							Checktype:     entriesArray[i].Report.ChecktypeName,
						}
					}
					if mv.Score < vuln.Score {
						mv.Vulnerability = vuln
					}

					mv.AffectedTargets = append(mv.AffectedTargets, entriesArray[i].Report.Target)

					mm[vuln.Severity()] = mv
					// NOTE: in the future when using the ID field of the Vulnerabilities, change by:
					// vmap[vuln.ID] = mm
					vmap[vuln.Summary] = mm
				}
				// Only process the last ocurrence of a scan.
				break
			}
		}
	}

	// Then we return that unique vulnerabilities in a list, instead of a map.
	var vulns []models.Vulnerability
	for _, m := range vmap {
		for _, v := range m {
			vulns = append(vulns, v)
		}
	}

	return vulns, nil
}

// GetTargetVulnerabilities retrieves the vulnerabilities of the given targets
// from the storage.
func (m *MemDB) GetTargetVulnerabilities(target ...string) ([]models.Vulnerability, error) {
	m.mux.RLock()
	historic := m.Historic
	m.mux.RUnlock()

	// First we iterate by all the reports stored in the historic table.
	// The goal is to find whether a vulnerability has been detected for more than
	// one Target and group them in that way. Also we need to take into account that
	// vulnerabilities with the same Summary but different Severity shouldn't be grouped.
	// To do that, we are using a map where the key is the summary of the vulnerability
	// and the value is another map. The key for the inner map is the SeverityRank, and
	// the value is the vulnerability + the list of the affected targets.
	vmap := make(map[string]map[report.SeverityRank]models.Vulnerability)
	for _, entriesArray := range historic {
		n := len(entriesArray)
		if n < 1 {
			continue
		}

		found := false
		for _, t := range target {
			// In the historic, every entriesArray value is for a specific target, so take
			// the last entry only.
			if entriesArray[n-1].Report.Target == t {
				found = true
				break
			}
		}
		if !found {
			continue
		}

		for _, vuln := range entriesArray[n-1].Report.Vulnerabilities {
			// NOTE: in the future when using the ID field of the Vulnerabilities, change by:
			// mm, ok := vmap[vuln.ID]
			mm, ok := vmap[vuln.Summary]
			if !ok {
				mm = make(map[report.SeverityRank]models.Vulnerability)
			}

			mv, ok := mm[vuln.Severity()]
			if !ok {
				mv = models.Vulnerability{
					Vulnerability: vuln,
					Checktype:     entriesArray[n-1].Report.ChecktypeName,
				}
			}
			if mv.Score < vuln.Score {
				mv.Vulnerability = vuln
			}

			mv.AffectedTargets = append(mv.AffectedTargets, entriesArray[n-1].Report.Target)

			mm[vuln.Severity()] = mv
			// NOTE: in the future when using the ID field of the Vulnerabilities, change by:
			// vmap[vuln.ID] = mm
			vmap[vuln.Summary] = mm
		}
	}

	// Then we return that unique vulnerabilities in a list, instead of a map.
	var vulns []models.Vulnerability
	for _, m := range vmap {
		for _, v := range m {
			vulns = append(vulns, v)
		}
	}

	return vulns, nil
}

// SaveState stores the current state of the DB in a file.
func (m *MemDB) SaveState(stateFile string) error {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	if err := enc.Encode(m); err != nil {
		return err
	}

	f, err := os.Create(stateFile)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}

	return nil
}

// LoadState retrieves current state of the DB from a file.
func LoadState(stateFile string) (*MemDB, error) {
	b, err := ioutil.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(b)
	dec := gob.NewDecoder(buf)

	m := MemDB{mux: &sync.RWMutex{}}
	err = dec.Decode(&m)
	return &m, err
}
