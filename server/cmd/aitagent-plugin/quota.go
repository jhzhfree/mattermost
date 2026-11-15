// See LICENSE.txt in the repository root for license information.

package main

import (
	"encoding/json"
	"errors"

	"github.com/mattermost/mattermost/server/public/model"
)

const (
	ledgerKey        = "intake_ledger_v2"
	maxLedgerBytes   = 2 * 1024 * 1024
	maxReceipts      = 128
	maxPending       = 64
	maxOrgPending    = 32
	maxUserPending   = 8
	maxRate          = 32
	maxOrgRate       = 16
	maxUserRate      = 4
	rateWindowMillis = 60 * 1000
	maxCASAttempts   = 8
	cancelledState   = "cancelled"
)

var (
	errUnavailable = errors.New("Receipt storage is unavailable. No execution or delivery was attempted.")
	errUncertain   = errors.New("Receipt storage outcome is uncertain. Check the receipt status before submitting again. No execution or delivery was attempted.")
	errBusy        = errors.New("Intake is busy or a replica is lagging; retry later. No execution or delivery was attempted.")
	errQuota       = errors.New("Intake rate, pending-request, or retained-receipt limit reached. Retry later or cancel an owned pending request; cancellation does not reset rate or retention limits.")
	errMissing     = errors.New("Receipt not available for the current user and binding.")
	errConflict    = errors.New("This command dispatch conflicts with an existing receipt. No execution or delivery was attempted.")
	errLegacy      = errors.New("Legacy receipt storage was found. Intake requires a quiesced, verified clean upgrade; no automatic migration or counter reset is supported.")
	errClock       = errors.New("Intake clock moved backwards relative to shared state. Check node clock synchronization before retrying.")
)

// One bounded row is the transaction boundary: no separate counter can be
// committed without its receipt, or released without its cancellation.
// Revision prevents ABA when the content otherwise returns to an earlier state.
type ledger struct {
	Version  int                 `json:"version"`
	Revision uint64              `json:"revision"`
	LastNow  int64               `json:"last_now"`
	Receipts map[string]*receipt `json:"receipts"`
}

func (p *Plugin) nowMillis() int64 {
	if p.clock != nil {
		return p.clock()
	}
	return model.GetMillis()
}

// mutate always confirms successful reads/writes against primary-side CAS.
// Replica lag may cause a bounded failure, never an unconditional overwrite.
func (p *Plugin) mutate(change func(*ledger, int64) (*receipt, error)) (*receipt, error) {
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		raw, appErr := p.API.KVGet(ledgerKey)
		if appErr != nil || len(raw) > maxLedgerBytes {
			return nil, errUnavailable
		}
		state := ledger{Version: 2, Receipts: make(map[string]*receipt)}
		if len(raw) == 0 {
			// This replica-side check is supplemental to the mandatory operator
			// upgrade acknowledgement. It cannot prove absence on the primary.
			keys, listErr := p.API.KVListWithOptions(0, 1, model.PluginKVListOptions{Prefix: "request_"})
			if listErr != nil {
				return nil, errUnavailable
			}
			if len(keys) != 0 {
				return nil, errLegacy
			}
			if raw != nil {
				return nil, errUnavailable
			} // never treat corrupt empty bytes as a missing key
		} else if json.Unmarshal(raw, &state) != nil || !state.valid() {
			return nil, errUnavailable
		}
		now := p.nowMillis()
		if now < state.LastNow || now <= 0 {
			return nil, errClock
		}
		pruned := false
		for id, r := range state.Receipts {
			if r.ExpiresAt <= now {
				delete(state.Receipts, id)
				pruned = true
			}
		}
		result, changeErr := change(&state, now)
		if changeErr != nil && !pruned {
			return nil, changeErr
		}
		state.Revision++
		state.LastNow = now
		data, err := json.Marshal(state)
		if err != nil || len(data) > maxLedgerBytes {
			return nil, errUnavailable
		}
		ok, writeErr := p.API.KVSetWithOptions(ledgerKey, data, model.PluginKVSetOptions{Atomic: true, OldValue: raw})
		if writeErr != nil {
			return nil, errUncertain
		} // may have committed; never compensate or retry blindly
		if ok {
			return result, changeErr
		}
	}
	return nil, errBusy
}

func (s *ledger) valid() bool {
	if s.Version != 2 || s.Receipts == nil || len(s.Receipts) > maxReceipts || s.Revision == 0 || s.Revision == ^uint64(0) || s.LastNow <= 0 {
		return false
	}
	for id, r := range s.Receipts {
		if r == nil || r.ID != id || !validReceiptID(id) || !model.IsValidId(r.ActorUserID) || r.OrganizationID == "" ||
			r.CreatedAt <= 0 || r.CreatedAt > s.LastNow || r.ExpiresAt != r.CreatedAt+receiptTTL*1000 ||
			(r.State != receiptState && r.State != cancelledState) {
			return false
		}
		if len(r.Goal) > 8192 || !validReceiptID(r.GoalHash) || (r.State == receiptState && r.GoalHash != digest(r.Goal)) || (r.State == cancelledState && r.Goal != "") {
			return false
		}
	}
	return true
}

func (s *ledger) hasCapacity(r *receipt, now int64) bool {
	if len(s.Receipts) >= maxReceipts {
		return false
	}
	pending, orgPending, userPending, rate, orgRate, userRate := 0, 0, 0, 0, 0, 0
	for _, old := range s.Receipts {
		if old.State == receiptState {
			pending++
			if old.OrganizationID == r.OrganizationID {
				orgPending++
			}
			if old.ActorUserID == r.ActorUserID {
				userPending++
			}
		}
		// Cancelled receipts still count toward the rolling admission rate.
		if old.CreatedAt > now-rateWindowMillis {
			rate++
			if old.OrganizationID == r.OrganizationID {
				orgRate++
			}
			if old.ActorUserID == r.ActorUserID {
				userRate++
			}
		}
	}
	return pending < maxPending && orgPending < maxOrgPending && userPending < maxUserPending && rate < maxRate && orgRate < maxOrgRate && userRate < maxUserRate
}

func (p *Plugin) admit(r *receipt, config configuration, args *model.CommandArgs) (*receipt, error) {
	return p.mutate(func(s *ledger, now int64) (*receipt, error) {
		if existing := s.Receipts[r.ID]; existing != nil {
			if !ownedReceiptAt(existing, r.ID, config, args, now) || existing.GoalHash != r.GoalHash {
				return nil, errConflict
			}
			return existing, nil
		}
		if !s.hasCapacity(r, now) {
			return nil, errQuota
		}
		r.CreatedAt, r.ExpiresAt = now, now+receiptTTL*1000
		s.Receipts[r.ID] = r
		return r, nil
	})
}

func (p *Plugin) lookup(id string, config configuration, args *model.CommandArgs, cancel bool) (*receipt, error) {
	return p.mutate(func(s *ledger, now int64) (*receipt, error) {
		r := s.Receipts[id]
		if !ownedReceiptAt(r, id, config, args, now) {
			return nil, errMissing
		}
		if cancel {
			r.State = cancelledState
			r.Goal = "" // retain hash/identity tombstone to prevent replay resurrection
		}
		return r, nil
	})
}
