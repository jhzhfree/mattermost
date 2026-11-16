// See LICENSE.txt in the repository root for license information.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

const (
	receiptTTL    = 7 * 24 * 60 * 60
	receiptState  = "awaiting_octop_authorization"
	usage         = "Use /aitagent request <goal> or /aitagent status <receipt-id> or /aitagent cancel <receipt-id>. Execution and external delivery are not connected."
	privacyNotice = "Do not put secrets or sensitive information in slash commands. Mattermost server audit logs may contain the full command, even when intake is rejected. The 7-day expiry applies only to plugin receipt access; storage cleanup is lazy and may be delayed; audit logs, exports and backups have separate retention."
)

// Plugin exposes explicit commands and a metrics-listener hook. It has no
// message hooks, user-facing ServeHTTP endpoint, scheduler, outbound client,
// or calls to create posts.
type Plugin struct {
	plugin.MattermostPlugin
	guard        processGuard
	sharedGuard  *processGuard
	budget       *workBudget
	workClock    func() time.Time
	retrySleep   func(time.Duration)
	clock        func() int64 // injected only by offline tests
	cachedConfig atomic.Pointer[configSnapshot]
	refreshing   atomic.Bool
}

type configuration struct {
	Enabled             bool
	QuotaStorageReady   bool
	OrganizationID      string
	EmployeeID          string
	BotUserID           string
	TeamID              string
	ChannelID           string
	AllowedRequesterIDs string
}

// receipt captures a request, not approval or evidence of task execution.
// The submitting human and the target digital employee are separate identities.
type receipt struct {
	Journal        *sourceJournal `json:"handoff_journal,omitempty"`
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	OperationMode  string         `json:"operation_mode"`
	ActorUserID    string         `json:"actor_user_id"`
	OrganizationID string         `json:"organization_id"`
	EmployeeID     string         `json:"employee_id"`
	BotUserID      string         `json:"bot_user_id"`
	TeamID         string         `json:"team_id"`
	ChannelID      string         `json:"channel_id"`
	BindingHash    string         `json:"binding_hash"`
	Goal           string         `json:"goal"`
	GoalHash       string         `json:"goal_hash"`
	State          string         `json:"state"`
	CreatedAt      int64          `json:"created_at"`
	ExpiresAt      int64          `json:"expires_at"`
}

func (p *Plugin) OnActivate() error {
	if err := p.OnConfigurationChange(); err != nil {
		return err
	}
	return p.API.RegisterCommand(&model.Command{
		Trigger:          "aitagent",
		AutoComplete:     true,
		AutoCompleteDesc: "Local intake only. No secrets or sensitive information: full commands may be audited beyond 7 days.",
		AutoCompleteHint: "[request <goal> | status <receipt-id> | cancel <receipt-id>]",
	})
}

func reply(text string) (*model.CommandResponse, *model.AppError) {
	return &model.CommandResponse{ResponseType: model.CommandResponseTypeEphemeral, Text: text + "\n\n" + privacyNotice}, nil
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (c configuration) valid() bool {
	if !c.Enabled || !c.QuotaStorageReady || strings.TrimSpace(c.OrganizationID) == "" || strings.TrimSpace(c.EmployeeID) == "" ||
		len(c.OrganizationID) > 128 || len(c.EmployeeID) > 128 ||
		!model.IsValidId(c.BotUserID) || !model.IsValidId(c.TeamID) || !model.IsValidId(c.ChannelID) {
		return false
	}
	if strings.TrimSpace(c.AllowedRequesterIDs) == "" || len(c.AllowedRequesterIDs) > 8192 {
		return false
	}
	for _, id := range strings.Split(c.AllowedRequesterIDs, ",") {
		if !model.IsValidId(strings.TrimSpace(id)) {
			return false
		}
	}
	return true
}

func (c configuration) allows(id string) bool {
	for _, allowed := range strings.Split(c.AllowedRequesterIDs, ",") {
		if strings.TrimSpace(allowed) == id {
			return true
		}
	}
	return false
}

func (c configuration) hash() string {
	data, _ := json.Marshal(c)
	return digest(string(data))
}

// authorize is deliberately called on every command, including receipt reads.
// The local allowlist permits intake only; it does not establish Octop access.
func (p *Plugin) authorize(c configuration, args *model.CommandArgs) bool {
	if args.TeamId != c.TeamID || args.ChannelId != c.ChannelID || !model.IsValidId(args.UserId) || !c.allows(args.UserId) {
		return false
	}
	user, appErr := p.API.GetUser(args.UserId)
	if appErr != nil || user == nil || user.Id != args.UserId || user.IsBot || user.DeleteAt != 0 {
		return false
	}
	bot, appErr := p.API.GetUser(c.BotUserID)
	if appErr != nil || bot == nil || bot.Id != c.BotUserID || !bot.IsBot || bot.DeleteAt != 0 {
		return false
	}
	channel, appErr := p.API.GetChannel(c.ChannelID)
	if appErr != nil || channel == nil || channel.Id != c.ChannelID || channel.TeamId != c.TeamID || channel.DeleteAt != 0 {
		return false
	}
	for _, id := range []string{args.UserId, c.BotUserID} {
		member, err := p.API.GetChannelMember(c.ChannelID, id)
		if err != nil || member == nil || member.UserId != id || member.ChannelId != c.ChannelID {
			return false
		}
	}
	return p.API.HasPermissionToChannel(args.UserId, c.ChannelID, model.PermissionCreatePost)
}

func (p *Plugin) ExecuteCommand(_ *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	if args == nil || len(args.Command) > 8192 || !model.IsValidId(args.UserId) {
		return reply(usage)
	}
	fields := strings.Fields(args.Command)
	if len(fields) < 2 || fields[0] != "/aitagent" || (fields[1] != "request" && fields[1] != "status" && fields[1] != "cancel") {
		return reply(usage)
	}
	guard := p.observation()
	if denial := p.prefilter(args); denial != "" {
		guard.prefilterRejected.Add(1)
		return reply(denial)
	}
	budget, denial := guard.enter(args.UserId, fields[1], p.wallNow)
	if budget == nil {
		return reply(denial)
	}
	defer guard.leave(budget)
	worker := &Plugin{clock: p.clock, workClock: p.workClock, retrySleep: p.retrySleep, budget: budget, sharedGuard: guard}
	worker.SetAPI(&budgetAPI{API: p.API, budget: budget})
	response, err := worker.executeCommand(args)
	if budget.expired() {
		text := budgetMessage
		if fields[1] == "request" && strings.TrimSpace(args.TriggerId) != "" {
			text += " Check /aitagent status " + digest(args.TriggerId+"\x00"+args.UserId+"\x00"+args.ChannelId) + "."
		} else if len(fields) == 3 && validReceiptID(fields[2]) {
			text += " Check /aitagent status " + fields[2] + "."
		}
		return reply(text)
	}
	return response, err
}

func (p *Plugin) executeCommand(args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	if args == nil || len(args.Command) > 8192 {
		return reply(usage)
	}
	fields := strings.Fields(args.Command)
	if len(fields) < 2 || fields[0] != "/aitagent" || (fields[1] != "request" && fields[1] != "status" && fields[1] != "cancel") {
		return reply(usage)
	}
	var config configuration
	if err := p.API.LoadPluginConfiguration(&config); err != nil || !config.valid() {
		return reply(configUnavailableMessage)
	}
	if !p.authorize(config, args) {
		return reply(notPermittedMessage)
	}
	if fields[1] == "status" || fields[1] == "cancel" {
		if len(fields) != 3 || !validReceiptID(fields[2]) {
			return reply(usage)
		}
		stored, err := p.lookup(fields[2], config, args, fields[1] == "cancel")
		if err != nil {
			return reply(err.Error())
		}
		return receiptReply(stored)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args.Command), "/aitagent"))
	goal := strings.TrimSpace(strings.TrimPrefix(rest, "request"))
	if !utf8.ValidString(goal) || len(goal) == 0 || utf8.RuneCountInString(goal) > 2000 || strings.TrimSpace(args.TriggerId) == "" {
		return reply("Provide a goal of 1–2000 characters using a new /aitagent request command.")
	}
	// Mattermost creates TriggerId before dispatching this hook. Hash it rather
	// than persisting the trigger token. This deduplicates the same dispatch;
	// a new client submission receives a new trigger and is a new request.
	id := digest(args.TriggerId + "\x00" + args.UserId + "\x00" + args.ChannelId)
	now := p.nowMillis()
	r := &receipt{
		ID: id, Source: "mattermost_plugin_command", OperationMode: "user_requested",
		ActorUserID: args.UserId, OrganizationID: config.OrganizationID, EmployeeID: config.EmployeeID,
		BotUserID: config.BotUserID, TeamID: args.TeamId, ChannelID: args.ChannelId, BindingHash: config.hash(),
		Goal: goal, GoalHash: digest(goal), State: receiptState, CreatedAt: now, ExpiresAt: now + receiptTTL*1000,
	}
	stored, err := p.admit(r, config, args)
	if err != nil {
		if err == errUncertain || err == errBusy {
			return reply(fmt.Sprintf("%s Check /aitagent status %s.", err.Error(), id))
		}
		return reply(err.Error())
	}
	return receiptReply(stored)
}

func validReceiptID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(id) == id
}

func ownedReceiptAt(r *receipt, id string, c configuration, args *model.CommandArgs, now int64) bool {
	return r != nil && r.ID == id && r.ActorUserID == args.UserId && r.TeamID == args.TeamId &&
		r.ChannelID == args.ChannelId && r.BindingHash == c.hash() && r.BotUserID == c.BotUserID &&
		r.OrganizationID == c.OrganizationID && r.EmployeeID == c.EmployeeID && r.ExpiresAt > now &&
		r.Source == "mattermost_plugin_command" && r.OperationMode == "user_requested" && (r.State == receiptState || r.State == cancelledState)
}

func receiptReply(r *receipt) (*model.CommandResponse, *model.AppError) {
	return reply(fmt.Sprintf("Receipt %s\nStatus: %s. Receipt access expires after 7 days; physical cleanup may be delayed. No task has been executed or delivered to Octop.", r.ID, r.State))
}
