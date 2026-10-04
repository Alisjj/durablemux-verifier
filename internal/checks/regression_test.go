package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Alisjj/durablemux-verifier/internal/config"
	"github.com/Alisjj/durablemux-verifier/internal/model"
	"github.com/Alisjj/durablemux-verifier/internal/runner"
)

func scriptContext(t *testing.T, body string) *runner.Context {
	t.Helper()
	project := t.TempDir()
	bin := filepath.Join(project, "dmux")
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg["binary"], cfg["timeout_seconds"] = bin, 0.4
	ctx, err := runner.New(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctx.Close)
	return ctx
}

func requireFailure(t *testing.T, results []model.CheckResult) {
	t.Helper()
	for _, r := range results {
		if !r.Passed {
			return
		}
	}
	t.Fatalf("broken implementation passed all checks: %+v", results)
}

func TestExecOnlyCannotPassPTYStages(t *testing.T) {
	for _, n := range []int{5, 6, 7, 8, 9, 10, 11} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := scriptContext(t, "#!/bin/sh\nif [ \"$1\" = run ]; then shift; shift; exec \"$@\"; fi\nexit 1\n")
			rs, err := RunBuiltin(fmt.Sprintf("stage_%d", n), ctx)
			if err != nil {
				t.Fatal(err)
			}
			requireFailure(t, rs)
		})
	}
}

func TestRealNestedPTYPassesTerminalProbes(t *testing.T) {
	source, err := os.ReadFile("../../tests/fixtures/pty_dmux.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{5, 6, 7, 8, 9, 10, 11} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := scriptContext(t, string(source))
			ctx.Timeout = 3 * time.Second
			rs, err := RunBuiltin(fmt.Sprintf("stage_%d", n), ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rs {
				if !r.Passed {
					t.Fatalf("%s: %s\n%s", r.Name, r.Detail, r.Stdout)
				}
			}
		})
	}
}

func TestRejectedCreationCannotPassStage12(t *testing.T) {
	requireFailure(t, stage12(scriptContext(t, "#!/bin/sh\necho rejected >&2\nexit 1\n")))
}

func TestEchoedInputCannotPassInteractiveShell(t *testing.T) {
	requireFailure(t, stage6(scriptContext(t, "#!/bin/sh\nread ignored\nexit 0\n")))
}

func TestAlreadyDeadProcessesCannotPassKill(t *testing.T) {
	ctx := scriptContext(t, `#!/usr/bin/env python3
import subprocess,sys
args=sys.argv[1:]
if args[0]=='new': subprocess.run(['sh','-c',args[-1].replace('sleep 60','sleep 0.01')],check=True)
sys.exit(0)
`)
	requireFailure(t, stage17(ctx))
}

func TestContradictoryExitRecordsCannotPassStage18(t *testing.T) {
	ctx := scriptContext(t, `#!/usr/bin/env python3
import json,sys
args=sys.argv[1:]
if args[0]=='inspect':
 name=args[1]
 if 'exit7' in name: print(json.dumps({'state':'running','exit_code':7.9}))
 elif 'exit0' in name: print(json.dumps({'state':'running','exit_code':0}))
 elif 'sigterm' in name: print('{}')
 else: sys.exit(1)
sys.exit(0)
`)
	for _, r := range stage18(ctx) {
		if r.Passed {
			t.Fatalf("invalid record passed: %s", r.Name)
		}
	}
}

func TestNoCoordinatorCannotPassStage20(t *testing.T) {
	requireFailure(t, stage20(scriptContext(t, "#!/bin/sh\nprintf '[]\\n'\nexit 0\n")))
}

func TestEchoOnlyAttachmentCannotPassAttachOrDetach(t *testing.T) {
	for _, n := range []int{14, 15} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := scriptContext(t, `#!/usr/bin/env python3
import os,pathlib,pty,signal,subprocess,sys,time
args=sys.argv[1:]
rt=pathlib.Path(os.environ['DMUX_RUNTIME_DIR'])
if args[0]=='serve':
 pid,master=pty.fork()
 if pid==0: os.execvp(args[2],args[2:])
 (rt/(args[1]+'.child')).write_text(str(pid))
 time.sleep(60)
elif args[0]=='new':
 server=subprocess.Popen([sys.executable,__file__,'serve',args[1]]+args[3:],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
 (rt/(args[1]+'.server')).write_text(str(server.pid))
elif args[0]=='attach':
 sys.stdin.readline()
elif args[0]=='kill':
 for suffix in ['.child','.server']:
  try: os.killpg(int((rt/(args[1]+suffix)).read_text()),signal.SIGKILL)
  except (FileNotFoundError,ProcessLookupError): pass
sys.exit(0)
`)
			ctx.Timeout = time.Second
			rs, err := RunBuiltin(fmt.Sprintf("stage_%d", n), ctx)
			if err != nil {
				t.Fatal(err)
			}
			requireFailure(t, rs)
		})
	}
}

func TestInvalidSessionListingCannotPassStage16(t *testing.T) {
	ctx := scriptContext(t, `#!/usr/bin/env python3
import json,os,pathlib,sys
args=sys.argv[1:]
f=pathlib.Path(os.environ['DMUX_RUNTIME_DIR'])/'names'
if args[0]=='new':
 with f.open('a') as out: out.write(args[1]+'\n')
elif args[0]=='list':
 if not f.exists(): print('[]')
 else: print(json.dumps([dict(id='',name=name,command=False,created_at='',attached_clients=-3,state='nonsense') for name in f.read_text().splitlines()]))
sys.exit(0)
`)
	requireFailure(t, stage16(ctx))
}

func TestTimeoutIsNotEvidenceOfNameRejection(t *testing.T) {
	ctx := scriptContext(t, "#!/bin/sh\nsleep 60\n")
	ctx.Timeout = 50 * time.Millisecond
	rs := stage12(ctx)
	if rs[1].Passed {
		t.Fatal("timed-out creation was treated as name validation")
	}
}

func TestSessionAndExitSchemas(t *testing.T) {
	ctx := scriptContext(t, "#!/bin/sh\nexit 0\n")
	valid := map[string]any{"id": "unique-a", "name": "a", "command": []any{"sleep", "60"}, "created_at": "2026-10-04T12:00:00Z", "attached_clients": float64(0), "state": "running"}
	if err := validateLiveSession(ctx, valid, "a"); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]any{"id": "", "command": false, "created_at": "", "attached_clients": float64(-1), "state": "nonsense"} {
		bad := map[string]any{}
		for k, v := range valid {
			bad[k] = v
		}
		bad[field] = value
		if err := validateLiveSession(ctx, bad, "a"); err == nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
	if _, err := sessionsFromJSON(ctx, []any{float64(123)}); err == nil {
		t.Fatal("non-object session entry accepted")
	}
	before := map[string]map[string]any{"a": valid}
	if !stableSessionIDs(ctx, before, []map[string]any{valid}) {
		t.Fatal("stable ID rejected")
	}
	if stableSessionIDs(ctx, before, []map[string]any{{"name": "a", "id": "changed"}}) {
		t.Fatal("changing ID accepted")
	}
	if !normalExit(ctx, map[string]any{"state": "exited", "exit_code": float64(7)}, 7) {
		t.Fatal("valid exit rejected")
	}
	if normalExit(ctx, map[string]any{"state": "exited", "exit_code": float64(7.9)}, 7) {
		t.Fatal("fractional exit accepted")
	}
	if !signalExit(ctx, map[string]any{"state": "terminated", "signal": "SIGTERM", "exit_code": float64(-1)}) {
		t.Fatal("valid signal exit rejected")
	}
	if signalExit(ctx, map[string]any{}) {
		t.Fatal("empty signal record accepted")
	}
}
