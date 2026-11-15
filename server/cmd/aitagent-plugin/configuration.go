// See LICENSE.txt in the repository root for license information.

package main

import (
	"errors"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
)

const configUnavailableMessage = "Digital employee intake is disabled or not configured; a configuration refresh may be pending or failed."
const notPermittedMessage = "This request is not permitted for the current user, employee, and channel binding."

// Immutable after publication. The map contains only administrator-configured
// IDs (bounded by configuration.valid), never IDs supplied by incoming traffic.
// This snapshot can reject work, but is never evidence that an action is allowed.
type configSnapshot struct {
	ready     bool
	teamID    string
	channelID string
	allowed   map[string]struct{}
}

func snapshotFor(c configuration) *configSnapshot {
	snapshot := &configSnapshot{}
	if !c.valid() {
		return snapshot
	}
	snapshot.ready = true
	snapshot.teamID, snapshot.channelID = c.TeamID, c.ChannelID
	snapshot.allowed = make(map[string]struct{})
	for _, id := range strings.Split(c.AllowedRequesterIDs, ",") {
		snapshot.allowed[strings.TrimSpace(id)] = struct{}{}
	}
	return snapshot
}

// The framework calls this on activation and configuration changes. Invalidate
// before the uncancellable load, allow at most one load, and never let an older
// result replace a newer invalidation. A failed/superseded load stays closed.
func (p *Plugin) OnConfigurationChange() error {
	ticket := &configSnapshot{}
	p.cachedConfig.Store(ticket)
	if !p.refreshing.CompareAndSwap(false, true) {
		p.observation().configRefreshFailed.Add(1)
		return errors.New("configuration refresh is already running; intake remains closed until a successful refresh")
	}
	defer p.refreshing.Store(false)
	var config configuration
	if err := p.API.LoadPluginConfiguration(&config); err != nil {
		p.observation().configRefreshFailed.Add(1)
		return errors.New("configuration refresh failed; intake remains closed")
	}
	if !p.cachedConfig.CompareAndSwap(ticket, snapshotFor(config)) {
		p.observation().configRefreshFailed.Add(1)
		return errors.New("configuration refresh was superseded; intake remains closed until a successful refresh")
	}
	return nil
}

func (p *Plugin) prefilter(args *model.CommandArgs) string {
	snapshot := p.cachedConfig.Load()
	if snapshot == nil || !snapshot.ready {
		return configUnavailableMessage
	}
	_, allowed := snapshot.allowed[args.UserId]
	if args.TeamId != snapshot.teamID || args.ChannelId != snapshot.channelID || !allowed {
		return notPermittedMessage
	}
	return ""
}
