package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buffsystems/buff-connector/internal/problem"
)

// The fake plugin is this test binary re-executed with an env marker, so the
// suite needs no interpreter, no fixtures on disk and no network. Each mode
// reproduces one thing a real plugin does — or does wrong.
func TestMain(m *testing.M) {
	if mode := os.Getenv("BUFF_FAKE_PLUGIN"); mode != "" {
		fakePluginMain(mode)
		return
	}
	os.Exit(m.Run())
}

func fakePluginMain(mode string) {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	reply := func(id json.Number, result any) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		out.Write(append(b, '\n'))
		out.Flush()
	}
	if mode == "stderr-then-die" {
		fmt.Fprintln(os.Stderr, "Traceback (most recent call last):")
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'buff_plugin'")
		os.Exit(1)
	}
	if mode == "noisy" {
		fmt.Fprintln(os.Stdout, "INFO plugin starting, this is not JSON")
		out.Flush()
	}
	if mode == "bilingual" {
		fakeBilingual(out)
		return
	}
	if mode == "plain-mcp" || mode == "cursor-loop" {
		fakePlainMCP(out, mode == "cursor-loop")
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var req struct {
			ID     json.Number     `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			if mode == "crash-on-init" {
				os.Exit(3)
			}
			hostAPI := 1
			if mode == "future-host" {
				hostAPI = 99
			}
			reply(req.ID, map[string]any{
				"protocolVersion": "2026-01-01",
				"hostApi":         hostAPI,
				"capabilities":    map[string]any{"tools": map[string]any{}, "buff/poll": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.2.3"},
			})
		case "tools/list":
			reply(req.ID, map[string]any{"tools": []any{
				map[string]any{
					"name": "search_issues", "description": "Search the tracker. Use when the task mentions a ticket key.",
					"inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": true},
				},
				map[string]any{
					"name": "create_issue", "description": "Create a ticket.",
					"inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": false},
				},
				map[string]any{
					"name": "render_frame", "description": "Render a frame as PNG.",
					"inputSchema": map[string]any{"type": "object"},
					"annotations": map[string]any{"readOnlyHint": true, "visionHint": true},
				},
			}})
		case "tools/call":
			if mode == "die-on-call" {
				os.Exit(4)
			}
			reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "PROJ-1 open"}}})
		case "buff/check":
			reply(req.ID, map[string]any{"ok": true, "detail": "connected as bot"})
		default:
			reply(req.ID, map[string]any{})
		}
	}
}

// fakePlainMCP is a server the operator brought: it speaks the published MCP
// protocol and knows nothing of buff/*. It is strict on purpose — a handshake
// the published SDKs would refuse must fail here too.
func fakePlainMCP(out *bufio.Writer, loop bool) {
	send := func(msg map[string]any) {
		b, _ := json.Marshal(msg)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	reply := func(id json.Number, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	refuse := func(id json.Number, why string) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32002, "message": why}})
	}
	tool := func(name string, ro bool) map[string]any {
		return map[string]any{"name": name, "description": name + " tool",
			"inputSchema": map[string]any{"type": "object"},
			"annotations": map[string]any{"readOnlyHint": ro}}
	}
	initialized, rootsAnswered := false, false
	type deferredList struct {
		id     json.Number
		params json.RawMessage
	}
	var deferred []deferredList
	serveTools := func(id json.Number, params json.RawMessage) {
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Cursor == "" || loop {
			reply(id, map[string]any{"tools": []any{tool("forecast", true), tool("alerts", true)}, "nextCursor": "page-2"})
		} else {
			reply(id, map[string]any{"tools": []any{tool("set_home", false)}})
		}
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var msg struct {
			ID     *json.Number    `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Method == "" {
			// The answer to our own roots/list: the connector has no roots to
			// offer and must SAY so, not leave the request hanging.
			if msg.ID != nil && msg.ID.String() == "1000" && len(msg.Error) > 0 {
				rootsAnswered = true
				for _, d := range deferred {
					serveTools(d.id, d.params)
				}
				deferred = nil
			}
			continue
		}
		if msg.ID == nil {
			if msg.Method == "notifications/initialized" {
				initialized = true
			}
			continue
		}
		id := *msg.ID
		switch msg.Method {
		case "initialize":
			var p map[string]json.RawMessage
			_ = json.Unmarshal(msg.Params, &p)
			if _, ok := p["capabilities"]; !ok {
				refuse(id, "initialize without capabilities")
				continue
			}
			if _, ok := p["clientInfo"]; !ok {
				refuse(id, "initialize without clientInfo")
				continue
			}
			if _, ok := p["instance"]; ok {
				refuse(id, "initialize carries a buff-only field")
				continue
			}
			reply(id, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "weather", "version": "0.3.0"},
			})
			send(map[string]any{"jsonrpc": "2.0", "id": 1000, "method": "roots/list"})
		case "tools/list":
			if !initialized {
				refuse(id, "tools/list before notifications/initialized")
				continue
			}
			// Ответ на наш roots/list и запрос tools/list идут навстречу друг другу, и
			// протокол не задаёт, какой придёт первым. Поэтому список не отдаётся, пока
			// roots/list без ответа, — запрос откладывается и выполняется, как только
			// ответ пришёл. Требование «не оставлять запрос висеть» сохранено: если
			// ответа не будет вовсе, клиент не дождётся списка и тест упадёт по таймауту.
			if !rootsAnswered {
				deferred = append(deferred, deferredList{id: id, params: msg.Params})
				continue
			}
			serveTools(id, msg.Params)
		case "tools/call":
			// A server the operator brought is sent exactly what MCP defines: no _meta of ours.
			if strings.Contains(string(msg.Params), "buff/lang") {
				refuse(id, "tools/call carries a buff-only field")
				continue
			}
			reply(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": "sunny"}}})
		default:
			refuse(id, "unknown method "+msg.Method)
		}
	}
}

func fakeSpec(mode string) Spec {
	return Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=TestNothingMatchesThis"},
		Env:  append(os.Environ(), "BUFF_FAKE_PLUGIN="+mode),
	}
}

func newInstance(t *testing.T, mode string, h Handler) *Instance {
	t.Helper()
	in := NewInstance(&Starter{Priv: Privilege{CanSwitchUID: false}}, InstanceConfig{
		GUID: "GUIDTEST", Slug: "fake", Spec: fakeSpec(mode),
		StartBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond,
		CallTimeout: 3 * time.Second,
	}, h)
	t.Cleanup(in.Stop)
	return in
}

func TestInstanceHandshakeExposesToolsAndReadOnlyFlag(t *testing.T) {
	in := newInstance(t, "plain", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	tools := in.Tools()
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
	byName := map[string]Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	if !byName["search_issues"].ReadOnly {
		t.Error("search_issues must be read-only")
	}
	if byName["create_issue"].ReadOnly {
		t.Error("create_issue must NOT be read-only: it writes into the customer's tracker")
	}
	// A picture is unreadable to a text-only model; the flag is what lets the
	// platform withhold the tool instead of handing over a blob.
	if !byName["render_frame"].Vision {
		t.Error("render_frame answers with a picture and must say so")
	}
	if byName["search_issues"].Vision {
		t.Error("search_issues answers in text and must NOT be withheld from a text-only model")
	}
	if in.State().Status != StatusRunning {
		t.Fatalf("status = %v", in.State().Status)
	}
}

// Noise on stdout is normal for a Python plugin and must not cost the handshake.
func TestInstanceSurvivesPluginLoggingToStdout(t *testing.T) {
	in := newInstance(t, "noisy", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(in.Tools()) == 0 {
		t.Fatal("handshake lost to log noise")
	}
}

func TestCallReachesTheTool(t *testing.T) {
	in := newInstance(t, "plain", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	var out struct {
		Content []struct{ Text string }
	}
	if err := in.CallTool(ctx, "search_issues", map[string]string{"jql": "x"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(out.Content) == 0 || !strings.Contains(out.Content[0].Text, "PROJ-1") {
		t.Fatalf("result = %+v", out)
	}
}

// A crash is not a reason to give up on an instance: it backs off and keeps
// trying, and the operator can see that it is doing so.
func TestInstanceRestartsAfterCrashAndStaysVisible(t *testing.T) {
	in := newInstance(t, "die-on-call", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The call kills the plugin; the error must reach the caller, not hang.
	if err := in.CallTool(ctx, "search_issues", nil, nil); err == nil {
		t.Fatal("call should fail when the plugin dies mid-call")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := in.State(); st.Status == StatusRunning && st.Restarts > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("instance did not come back: %+v", in.State())
}

// Backoff grows but is bounded: two crashing instances must not spin a small
// customer VM, and they must never be switched off either.
func TestBackoffIsCappedAndNeverGivesUp(t *testing.T) {
	in := newInstance(t, "crash-on-init", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = in.Start(ctx)

	time.Sleep(600 * time.Millisecond)
	st := in.State()
	if st.Restarts < 3 {
		t.Fatalf("restarts = %d, want the supervisor to keep trying", st.Restarts)
	}
	if st.Backoff > 40*time.Millisecond {
		t.Fatalf("backoff = %v, want it capped at MaxBackoff", st.Backoff)
	}
	if st.Status == StatusStopped {
		t.Fatal("a crashing instance must never be switched off on its own")
	}
	if st.LastError == "" {
		t.Error("the operator needs the reason, not just a status")
	}
}

// Without the privilege to isolate, we do not start the plugin at all — and the
// state says exactly why, so it reaches the admin UI and the report.
func TestInstanceRefusesToRunUnisolated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uid semantics")
	}
	spec := fakeSpec("plain")
	spec.UID, spec.GID = 61234, 61234
	in := NewInstance(&Starter{Priv: Privilege{CanSwitchUID: false, Reason: problem.New("no_isolation_uid", 1000)}},
		InstanceConfig{GUID: "G", Slug: "fake", Spec: spec, CallTimeout: time.Second}, nil)
	defer in.Stop()

	err := in.Start(context.Background())
	if err == nil {
		t.Fatal("must refuse to run a plugin it cannot isolate")
	}
	st := in.State()
	if st.Status != StatusNoIsolation {
		t.Fatalf("status = %v, want StatusNoIsolation", st.Status)
	}
	if !strings.Contains(st.LastError, "uid 1000") {
		t.Errorf("state must carry the operator-facing reason, got %q", st.LastError)
	}
	// And the report carries it as a status, down to the reason's own code.
	if p := st.LastProblem; p == nil || p.Code != "no_isolation" || p.Cause == nil ||
		p.Cause.Code != "no_isolation_uid" || p.Cause.Params["uid"] != "1000" {
		t.Errorf("LastProblem = %+v", p)
	}
}

// The plugin asks the host to do things it is not trusted to do itself.
func TestPluginCallbacksReachTheHostHandler(t *testing.T) {
	seen := make(chan string, 4)
	in := newInstance(t, "plain", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		seen <- method
		return map[string]any{"ok": true}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The handler is wired even when this particular fake never calls back; the
	// contract under test is that the host side is installed, not that the fake
	// uses it.
	if in.handler == nil {
		t.Fatal("host handler not wired into the channel")
	}
}

// A plugin that dies during the handshake must hand the operator its traceback,
// not "the channel closed".
func TestStartupFailureCarriesWhatThePluginPrinted(t *testing.T) {
	in := newInstance(t, "stderr-then-die", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := in.Start(ctx)
	if err == nil {
		t.Fatal("expected the start to fail")
	}
	if !strings.Contains(err.Error(), "ModuleNotFoundError") {
		t.Fatalf("err = %v, want it to carry the plugin's own output", err)
	}
	if len(in.Log()) == 0 {
		t.Error("the instance log should hold what the plugin wrote")
	}
}

// A connector must know what an instance OFFERS before it starts asking. Polling
// a plugin that reports nothing is a call that fails on every tick, and routing
// a reply to one that cannot reply loses the answer somewhere quiet.
// A plugin built for a host this connector is not yet is refused by NAME and
// NUMBER, not by a call that fails later on some quiet path.
func TestPluginNeedingANewerHostIsRefusedWithBothNumbers(t *testing.T) {
	in := newInstance(t, "future-host", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := in.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "99") ||
		!strings.Contains(err.Error(), fmt.Sprint(SupportedHostAPI)) {
		t.Fatalf("err = %v, want both host API numbers", err)
	}
}

func TestInstanceRemembersWhatThePluginOffers(t *testing.T) {
	in := newInstance(t, "plain", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	info := in.Info()
	if !info.Polls {
		t.Error("the plugin declared buff/poll and the instance forgot it")
	}
	if info.Replies {
		t.Error("buff/reply was not declared but is reported as available")
	}
}

// A server the operator brought speaks the published MCP protocol and nothing
// else. The handshake has to be the standard one, the server's own requests
// have to be answered rather than left hanging, every page of tools has to be
// read, and what counts as read-only is the OPERATOR's decision — the server's
// claim is a hint, shown but not trusted.
func TestPlainMCPServerIsSpokenToByTheBook(t *testing.T) {
	in := NewInstance(&Starter{Priv: Privilege{CanSwitchUID: false}}, InstanceConfig{
		GUID: "GUIDTEST", Slug: "weather", Spec: fakeSpec("plain-mcp"), PlainMCP: true,
		StartBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond,
		CallTimeout: 3 * time.Second,
	}, nil)
	t.Cleanup(in.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := in.Info().Name; got != "weather" {
		t.Errorf("serverInfo.name = %q", got)
	}

	byName := func() map[string]Tool {
		m := map[string]Tool{}
		for _, tl := range in.Tools() {
			m[tl.Name] = tl
		}
		return m
	}
	tools := byName()
	if len(tools) != 3 {
		t.Fatalf("tools = %v, want both pages", tools)
	}
	if !tools["forecast"].ReadOnlyHint || tools["set_home"].ReadOnlyHint {
		t.Error("the server's readOnlyHint must be kept as the hint it is")
	}
	for name, tl := range tools {
		if tl.ReadOnly {
			t.Errorf("%s is read-only before the operator said so", name)
		}
	}

	in.SetReadOnlyTools([]string{"forecast"})
	tools = byName()
	if !tools["forecast"].ReadOnly {
		t.Error("the operator's mark must make the tool read-only")
	}
	if tools["alerts"].ReadOnly {
		t.Error("a hint alone must not make a tool read-only")
	}

	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := in.CallTool(ctx, "forecast", map[string]any{"city": "Perm"}, &res); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "sunny" {
		t.Fatalf("result = %+v", res)
	}
}

// A catalogue plugin is trusted on its word: its hint IS the decision, as before.
func TestCataloguePluginHintIsItsReadOnlyDecision(t *testing.T) {
	in := newInstance(t, "plain", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := in.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, tl := range in.Tools() {
		if tl.ReadOnly != tl.ReadOnlyHint {
			t.Errorf("%s: read-only %v, hint %v — a catalogue plugin's hint is its decision", tl.Name, tl.ReadOnly, tl.ReadOnlyHint)
		}
	}
}

// A server that hands back the same cursor for ever is a list that never ends;
// the connector says so instead of looping.
func TestARepeatingCursorIsRefusedNotFollowedForever(t *testing.T) {
	in := NewInstance(&Starter{Priv: Privilege{CanSwitchUID: false}}, InstanceConfig{
		GUID: "GUIDTEST", Slug: "loop", Spec: fakeSpec("cursor-loop"), PlainMCP: true,
		StartBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, CallTimeout: 3 * time.Second,
	}, nil)
	t.Cleanup(in.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := in.Start(ctx)
	if err == nil || !errors.Is(err, problem.New("tools_cursor_loop")) {
		t.Fatalf("err = %v", err)
	}
}
