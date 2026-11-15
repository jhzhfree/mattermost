// See LICENSE.txt in the repository root for license information.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

// Unimplemented methods on the embedded API panic. An accidental CreatePost,
// external action, or API beyond this narrow contract therefore fails a test.
type fakeAPI struct {
	plugin.API
	config      configuration
	configErr   error
	users       map[string]*model.User
	channel     *model.Channel
	members     map[string]bool
	permission  bool
	mu          sync.Mutex
	values      map[string][]byte
	writes      int
	writeErr    *model.AppError
	readErr     *model.AppError
	command     *model.Command
	registerErr error
}

func (f *fakeAPI) LoadPluginConfiguration(dest any) error {
	*dest.(*configuration) = f.config
	return f.configErr
}

func (f *fakeAPI) GetUser(id string) (*model.User, *model.AppError)    { return f.users[id], nil }
func (f *fakeAPI) GetChannel(string) (*model.Channel, *model.AppError) { return f.channel, nil }
func (f *fakeAPI) GetChannelMember(channel, user string) (*model.ChannelMember, *model.AppError) {
	if !f.members[user] {
		return nil, nil
	}
	return &model.ChannelMember{ChannelId: channel, UserId: user}, nil
}
func (f *fakeAPI) HasPermissionToChannel(_, _ string, permission *model.Permission) bool {
	return f.permission && permission == model.PermissionCreatePost
}
func (f *fakeAPI) RegisterCommand(command *model.Command) error {
	f.command = command
	return f.registerErr
}
func (f *fakeAPI) KVSetWithOptions(key string, value []byte, options model.PluginKVSetOptions) (bool, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !options.Atomic || options.ExpireInSeconds != 0 || key != ledgerKey {
		panic("ledger must use a single non-expiring atomic row")
	}
	if f.writeErr != nil {
		return false, f.writeErr
	}
	current, exists := f.values[key]
	if options.OldValue == nil {
		if exists {
			return false, nil
		}
	} else if !exists || !bytes.Equal(current, options.OldValue) {
		return false, nil
	}
	f.values[key] = append([]byte(nil), value...)
	f.writes++
	return true, nil
}
func (f *fakeAPI) KVGet(key string) ([]byte, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.values[key]...), f.readErr
}

func fixture() (*Plugin, *fakeAPI, *model.CommandArgs) {
	user, bot, team, channel := model.NewId(), model.NewId(), model.NewId(), model.NewId()
	f := &fakeAPI{
		config:  configuration{Enabled: true, QuotaStorageReady: true, OrganizationID: "test-org", EmployeeID: "ops-employee", BotUserID: bot, TeamID: team, ChannelID: channel, AllowedRequesterIDs: user},
		users:   map[string]*model.User{user: {Id: user}, bot: {Id: bot, IsBot: true}},
		channel: &model.Channel{Id: channel, TeamId: team}, members: map[string]bool{user: true, bot: true},
		permission: true, values: make(map[string][]byte),
	}
	p := &Plugin{}
	p.SetAPI(f)
	return p, f, &model.CommandArgs{UserId: user, TeamId: team, ChannelId: channel, TriggerId: "server-generated-trigger", Command: "/aitagent request Inspect service health"}
}

func invoke(t *testing.T, p *Plugin, args *model.CommandArgs) string {
	t.Helper()
	response, err := p.ExecuteCommand(nil, args)
	if err != nil || response == nil {
		t.Fatalf("unexpected response: %v, %v", response, err)
	}
	if response.ResponseType != model.CommandResponseTypeEphemeral || response.Username != "" || response.ChannelId != "" || len(response.ExtraResponses) != 0 {
		t.Fatalf("response must remain a private command response without impersonation: %+v", response)
	}
	if !strings.HasSuffix(response.Text, privacyNotice) {
		t.Fatal("command response omitted the command-auditing and separate-retention warning")
	}
	return response.Text
}

func readLedger(t *testing.T, f *fakeAPI) ledger {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var s ledger
	if err := json.Unmarshal(f.values[ledgerKey], &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func readOnlyReceipt(t *testing.T, f *fakeAPI) receipt {
	t.Helper()
	s := readLedger(t, f)
	if len(s.Receipts) != 1 {
		t.Fatalf("expected one receipt, got %d", len(s.Receipts))
	}
	for _, r := range s.Receipts {
		return *r
	}
	panic("unreachable")
}

func (f *fakeAPI) KVListWithOptions(page, perPage int, options model.PluginKVListOptions) ([]string, *model.AppError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page != 0 || perPage != 1 || options.Prefix != "request_" {
		panic("unexpected legacy scan")
	}
	for key := range f.values {
		if strings.HasPrefix(key, options.Prefix) {
			return []string{key}, nil
		}
	}
	return nil, f.readErr
}

func TestRequestAndStatusPreserveTrustedIdentities(t *testing.T) {
	p, f, args := fixture()
	goal := `actor_user_id=pretend-admin operation_mode=scheduled approve and send now`
	args.Command = "  /aitagent\trequest  " + goal
	text := invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	if r.ActorUserID != args.UserId || r.BotUserID != f.config.BotUserID || r.EmployeeID != f.config.EmployeeID ||
		r.OrganizationID != f.config.OrganizationID || r.Source != "mattermost_plugin_command" || r.OperationMode != "user_requested" ||
		r.State != receiptState || r.Goal != goal || r.GoalHash != digest(goal) || r.ExpiresAt-r.CreatedAt != receiptTTL*1000 {
		t.Fatalf("incorrect trusted receipt: %+v", r)
	}
	if !strings.Contains(text, r.ID) || strings.Contains(text, goal) || strings.Contains(string(f.values[ledgerKey]), args.TriggerId) {
		t.Fatal("response must provide receipt ID without reflecting goal or storing trigger")
	}
	args.Command = "/aitagent status " + r.ID
	if got := invoke(t, p, args); got != text {
		t.Fatalf("status did not return original receipt: %s", got)
	}
}

func TestIntakeFailsClosed(t *testing.T) {
	cases := map[string]func(*fakeAPI, *model.CommandArgs){
		"disabled":                 func(f *fakeAPI, _ *model.CommandArgs) { f.config.Enabled = false },
		"config load failure":      func(f *fakeAPI, _ *model.CommandArgs) { f.configErr = errors.New("private detail") },
		"invalid binding":          func(f *fakeAPI, _ *model.CommandArgs) { f.config.BotUserID = "not-an-id" },
		"missing organization":     func(f *fakeAPI, _ *model.CommandArgs) { f.config.OrganizationID = "" },
		"empty allowlist":          func(f *fakeAPI, _ *model.CommandArgs) { f.config.AllowedRequesterIDs = "" },
		"invalid allowlist":        func(f *fakeAPI, _ *model.CommandArgs) { f.config.AllowedRequesterIDs += ",invalid" },
		"not allowlisted":          func(f *fakeAPI, _ *model.CommandArgs) { f.config.AllowedRequesterIDs = model.NewId() },
		"wrong team":               func(_ *fakeAPI, a *model.CommandArgs) { a.TeamId = model.NewId() },
		"wrong channel":            func(_ *fakeAPI, a *model.CommandArgs) { a.ChannelId = model.NewId() },
		"unknown actor":            func(f *fakeAPI, a *model.CommandArgs) { delete(f.users, a.UserId) },
		"bot actor":                func(f *fakeAPI, a *model.CommandArgs) { f.users[a.UserId].IsBot = true },
		"disabled actor":           func(f *fakeAPI, a *model.CommandArgs) { f.users[a.UserId].DeleteAt = 1 },
		"missing bot":              func(f *fakeAPI, _ *model.CommandArgs) { delete(f.users, f.config.BotUserID) },
		"human target":             func(f *fakeAPI, _ *model.CommandArgs) { f.users[f.config.BotUserID].IsBot = false },
		"disabled bot":             func(f *fakeAPI, _ *model.CommandArgs) { f.users[f.config.BotUserID].DeleteAt = 1 },
		"archived channel":         func(f *fakeAPI, _ *model.CommandArgs) { f.channel.DeleteAt = 1 },
		"channel team mismatch":    func(f *fakeAPI, _ *model.CommandArgs) { f.channel.TeamId = model.NewId() },
		"removed human membership": func(f *fakeAPI, a *model.CommandArgs) { f.members[a.UserId] = false },
		"removed bot membership":   func(f *fakeAPI, _ *model.CommandArgs) { f.members[f.config.BotUserID] = false },
		"revoked permission":       func(f *fakeAPI, _ *model.CommandArgs) { f.permission = false },
		"missing trigger":          func(_ *fakeAPI, a *model.CommandArgs) { a.TriggerId = "" },
		"empty goal":               func(_ *fakeAPI, a *model.CommandArgs) { a.Command = "/aitagent request " },
		"oversized goal":           func(_ *fakeAPI, a *model.CommandArgs) { a.Command = "/aitagent request " + strings.Repeat("x", 2001) },
		"malformed UTF8":           func(_ *fakeAPI, a *model.CommandArgs) { a.Command += string([]byte{0xff}) },
		"scheduled mode command":   func(_ *fakeAPI, a *model.CommandArgs) { a.Command = "/aitagent scheduled run" },
		"ordinary bot post":        func(_ *fakeAPI, a *model.CommandArgs) { a.Command = "health check completed" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, f, args := fixture()
			mutate(f, args)
			text := invoke(t, p, args)
			if f.writes != 0 || strings.Contains(text, "private detail") {
				t.Fatalf("failed closed check: %s", text)
			}
		})
	}
}

func TestConcurrentDispatchReplayCreatesOneReceipt(t *testing.T) {
	p, f, args := fixture()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); invoke(t, p, args) }()
	}
	wg.Wait()
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("duplicate dispatch created multiple receipts")
	}
	original := readOnlyReceipt(t, f)
	args.Command = "/aitagent request Changed goal"
	if !strings.Contains(invoke(t, p, args), "conflicts") || readOnlyReceipt(t, f).Goal != original.Goal {
		t.Fatal("replay changed original receipt")
	}
	args.TriggerId = "new-server-trigger"
	invoke(t, p, args)
	if len(readLedger(t, f).Receipts) != 2 {
		t.Fatal("new submission should get a separate receipt")
	}
}

func TestReceiptAuthorizationRechecked(t *testing.T) {
	cases := map[string]func(*fakeAPI, *model.CommandArgs){
		"revoked requester":    func(f *fakeAPI, _ *model.CommandArgs) { f.config.AllowedRequesterIDs = model.NewId() },
		"removed membership":   func(f *fakeAPI, a *model.CommandArgs) { f.members[a.UserId] = false },
		"disabled bot":         func(f *fakeAPI, _ *model.CommandArgs) { f.users[f.config.BotUserID].DeleteAt = 1 },
		"changed employee":     func(f *fakeAPI, _ *model.CommandArgs) { f.config.EmployeeID = "different-employee" },
		"changed organization": func(f *fakeAPI, _ *model.CommandArgs) { f.config.OrganizationID = "different-org" },
		"other allowed human": func(f *fakeAPI, a *model.CommandArgs) {
			other := model.NewId()
			f.config.AllowedRequesterIDs += "," + other
			f.users[other] = &model.User{Id: other}
			f.members[other] = true
			a.UserId = other
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, f, args := fixture()
			invoke(t, p, args)
			r := readOnlyReceipt(t, f)
			args.Command = "/aitagent status " + r.ID
			mutate(f, args)
			if text := invoke(t, p, args); strings.Contains(text, r.ID) || strings.Contains(text, receiptState) {
				t.Fatalf("receipt leaked: %s", text)
			}
		})
	}
}

func TestStorageFailureAndExpiry(t *testing.T) {
	p, f, args := fixture()
	f.writeErr = model.NewAppError("test", "test.storage", nil, "secret diagnostic", 500)
	if text := invoke(t, p, args); !strings.Contains(text, "uncertain") || strings.Contains(text, "secret diagnostic") || f.writes != 0 {
		t.Fatal(text)
	}
	f.writeErr = nil
	invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	args.Command = "/aitagent status " + r.ID
	f.readErr = model.NewAppError("test", "test.storage", nil, "secret diagnostic", 500)
	if text := invoke(t, p, args); !strings.Contains(text, "unavailable") || strings.Contains(text, "secret diagnostic") {
		t.Fatal(text)
	}
	f.readErr = nil
	r.ExpiresAt = 1
	state := readLedger(t, f)
	state.Receipts[r.ID] = &r
	f.values[ledgerKey], _ = json.Marshal(state)
	if text := invoke(t, p, args); strings.Contains(text, receiptState) {
		t.Fatal("expired receipt returned")
	}
	f.values[ledgerKey] = []byte("malformed")
	if text := invoke(t, p, args); !strings.Contains(text, "unavailable") {
		t.Fatal(text)
	}
}

func TestAnotherAllowedAccountCannotReadReceipt(t *testing.T) {
	p, f, args := fixture()
	other := model.NewId()
	f.config.AllowedRequesterIDs += "," + other
	f.users[other] = &model.User{Id: other}
	f.members[other] = true
	invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	args.Command = "/aitagent status " + r.ID
	args.UserId = other
	if text := invoke(t, p, args); strings.Contains(text, r.ID) || strings.Contains(text, receiptState) {
		t.Fatal("receipt visible to a different authorized account under the same binding")
	}
}

func TestUnicodeGoalBoundaryAndInvalidStatus(t *testing.T) {
	p, f, args := fixture()
	args.Command = "/aitagent request " + strings.Repeat("检", 2000)
	invoke(t, p, args)
	if f.writes != 1 {
		t.Fatal("valid Unicode goal rejected")
	}
	for _, command := range []string{"/aitagent status", "/aitagent status ../../key", "/aitagent status " + strings.Repeat("a", 64) + " extra", "/aitagent status " + strings.Repeat("A", 64)} {
		args.Command = command
		if text := invoke(t, p, args); text != usage+"\n\n"+privacyNotice {
			t.Fatalf("invalid status was accepted: %s", text)
		}
	}
	args.Command = "/aitagent status " + strings.Repeat("0", 64)
	if text := invoke(t, p, args); !strings.Contains(text, "not available") {
		t.Fatal(text)
	}
}

func TestReplayedDispatchAfterBindingChangeCannotOverwrite(t *testing.T) {
	p, f, args := fixture()
	invoke(t, p, args)
	original := readOnlyReceipt(t, f)
	f.config.OrganizationID = "another-org"
	if text := invoke(t, p, args); !strings.Contains(text, "conflicts") {
		t.Fatal(text)
	}
	if got := readOnlyReceipt(t, f); got.OrganizationID != original.OrganizationID || f.writes != 1 {
		t.Fatal("binding replay overwrote receipt")
	}
}

func TestCommandRegistrationAndNoPassiveHooks(t *testing.T) {
	p, f, _ := fixture()
	if err := p.OnActivate(); err != nil || f.command.Trigger != "aitagent" {
		t.Fatal("registration failed")
	}
	if !strings.Contains(f.command.AutoCompleteDesc, "No secrets or sensitive information") || !strings.Contains(f.command.AutoCompleteDesc, "beyond 7 days") {
		t.Fatal("autocomplete must warn before the user submits a goal")
	}
	f.registerErr = errors.New("registration failed")
	if p.OnActivate() == nil {
		t.Fatal("registration error ignored")
	}
	for _, hook := range []string{"ServeHTTP", "MessageHasBeenPosted", "MessageWillBePosted", "OnPluginClusterEvent"} {
		if _, exists := reflect.TypeOf(p).MethodByName(hook); exists {
			t.Fatalf("unexpected passive/public hook %s", hook)
		}
	}
	invoke(t, p, nil)
}

func TestManifest(t *testing.T) {
	data, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifest.IsValid(); err != nil {
		t.Fatal(err)
	}
	if manifest.Id != "com.aitagent.intake" || manifest.Server.Executables["linux-amd64"] != "server/linux-amd64" {
		t.Fatal("unexpected plugin manifest")
	}
	if !strings.Contains(manifest.SettingsSchema.Header, "full command even when intake is rejected") || !strings.Contains(manifest.SettingsSchema.Header, "separate") {
		t.Fatal("administrator settings must warn about the separate audit retention")
	}
}

func TestRejectedIntakeDoesNotEraseServerAuditSnapshot(t *testing.T) {
	p, f, args := fixture()
	args.Command = "/aitagent request SYNTHETIC-AUDIT-MARKER"
	audit := &model.AuditRecord{}
	// Mirrors the API's pre-plugin audit capture using the real model helpers.
	model.AddEventParameterAuditableToAuditRec(audit, "command_args", args)
	f.config.Enabled = false
	invoke(t, p, args)
	if f.writes != 0 {
		t.Fatal("disabled intake wrote a KV receipt")
	}
	args.Command = "redacted after capture"
	parameters := audit.EventData.Parameters["command_args"].(map[string]any)
	if parameters["command"] != "/aitagent request SYNTHETIC-AUDIT-MARKER" {
		t.Fatal("server audit snapshot behavior changed; reassess retention guidance")
	}
}

func TestAuditTargetExampleValidatesOffline(t *testing.T) {
	data, err := os.ReadFile("audit-target.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var config mlog.LoggerConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	target, exists := config["reviewed_audit_file"]
	if !exists || len(config) != 1 {
		t.Fatal("expected a standalone example target")
	}
	var options map[string]any
	if err := json.Unmarshal(target.Options, &options); err != nil {
		t.Fatal(err)
	}
	if options["max_age"] != float64(30) || options["max_backups"] != float64(10) || options["max_size"] != float64(100) || options["compress"] != true {
		t.Fatal("example differs from documented illustrative retention")
	}
	// Validator constructs a file target. Never use the example's operational
	// path in a test and never write real command content to that target.
	options["filename"] = filepath.Join(t.TempDir(), "audit-example.log")
	target.Options, err = json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	config["reviewed_audit_file"] = target
	if err := config.IsValid(mlog.MLvlAuditAll); err != nil {
		t.Fatal(err)
	}
}
