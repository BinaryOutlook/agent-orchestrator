#!/usr/bin/env python3
"""Drive the #6299 fold repro against one isolated AO lab and record evidence.

Usage: repro.py <lab> <port> <cli> <runfile> <datadir> <evidence_dir> <cdp_port> [observe_seconds]
"""
import datetime
import json
import os
import subprocess
import sys
import time
import urllib.request

lab, port, cli, runfile, datadir, ev, cdp = sys.argv[1:8]
observe = int(sys.argv[8]) if len(sys.argv) > 8 else 300
base = f"http://127.0.0.1:{port}/api/v1"
env = {k: v for k, v in os.environ.items() if not k.startswith("CLAUDE") and k != "ELECTRON_RUN_AS_NODE"}
env.update(AO_RUN_FILE=runfile, AO_DATA_DIR=datadir, AO_PORT=port)
os.makedirs(ev, exist_ok=True)
log = open(f"{ev}/{lab}-timeline.jsonl", "a")

PROMPT = (
    "Step 1: run the Bash command `sleep 10` with run_in_background set to true. "
    'Step 2: immediately end your turn by replying with just the word "started". '
    "Later, when you receive the task notification that the background command finished, "
    "run this Bash command in the foreground (do not run it in the background): "
    "`for i in $(seq 1 60); do sleep 1; done; echo loop-done`, then run `echo second-step`, "
    'then reply "cycle done". If you receive any other user message, answer it briefly in one line.'
)
QUESTION = "quick question: what is 2+2?"


def now():
    return datetime.datetime.now(datetime.timezone.utc)


def stamp(t=None):
    return (t or now()).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def note(event, **kw):
    rec = {"t": stamp(), "event": event, **kw}
    print(json.dumps(rec), flush=True)
    log.write(json.dumps(rec) + "\n")
    log.flush()


def get(path):
    with urllib.request.urlopen(base + path, timeout=15) as r:
        d = json.load(r)
    return d.get("data", d)


def conv(sid):
    return get(f"/sessions/{sid}/conversation")


def ao(*args):
    return subprocess.run([cli, *args], env=env, capture_output=True, text=True, timeout=180)


def shot(sid, name, wait_ms=2500):
    out = f"{ev}/{lab}-{name}.png"
    r = subprocess.run(
        ["node", os.path.expanduser("~/.ao/dev/pr6270-lab/shot.cjs"), cdp, out, f"/sessions/{sid}", str(wait_ms)],
        capture_output=True, text=True, timeout=120,
    )
    note("screenshot", file=out, ok=r.returncode == 0, err=r.stderr[-300:] if r.returncode else "")


def session_state(sid):
    try:
        s = get(f"/sessions/{sid}")
    except Exception as e:  # evidence helper only; never abort the run on it
        return {"error": str(e)}
    s = s.get("session", s)
    act = s.get("activity")
    return {"status": s.get("status"), "activity": act.get("state") if isinstance(act, dict) else act}


def turn_summary(c):
    return [{k: t.get(k) for k in ("id", "state", "requestedAt", "startedAt", "completedAt")} for t in c.get("turns", [])]


def assistant_texts(c):
    return [(m.get("createdAt"), (m.get("text") or "")[:80], m.get("turnId")) for m in c.get("messages", []) if m.get("role") == "assistant"]


# 1. spawn
r = ao("spawn", "--standalone", "--harness", "claude-code", "--mode", "chat", "--model", "claude-sonnet-5-5",
       "--name", f"repro stuck Working ({lab})", "--prompt", PROMPT)
note("spawn", rc=r.returncode, out=r.stdout[-400:], err=r.stderr[-400:])
sessions = get("/sessions")["sessions"] if isinstance(get("/sessions"), dict) else []
sid = sorted(sessions, key=lambda s: s.get("createdAt", ""))[-1]["id"]
note("session", id=sid)

# 2. wait for turn 1 to complete ("started")
deadline = time.time() + 300
while time.time() < deadline:
    c = conv(sid)
    turns = c.get("turns", [])
    if turns and turns[0].get("state") == "completed":
        note("turn1_completed", turns=turn_summary(c), assistant=assistant_texts(c)[-1:])
        break
    time.sleep(1)
else:
    note("abort", reason="turn 1 never completed", turns=turn_summary(conv(sid)))
    sys.exit(2)
t1_done = turns[0]["completedAt"]

# 3. wait for the self-started task-notification cycle (out-of-turn loop command)
deadline = time.time() + 180
cycle = None
while time.time() < deadline:
    c = conv(sid)
    for a in c.get("activities", []):
        summ = a.get("summary") or ""
        if not a.get("turnId") and ("seq 1 60" in summ or "loop-done" in summ):
            cycle = a
            break
    if cycle:
        note("cycle_started", activity={k: cycle.get(k) for k in ("summary", "status", "createdAt", "turnId")},
             controller=c.get("controller"), session=session_state(sid))
        break
    time.sleep(1)
if not cycle:
    note("abort", reason="no out-of-turn loop activity", activities=[(a.get("summary"), a.get("turnId"), a.get("status")) for a in conv(sid).get("activities", [])][-6:])
    sys.exit(3)

# 4. send the user message while the cycle runs
time.sleep(6)
c = conv(sid)
note("pre_send", controller=c.get("controller"), session=session_state(sid), turns=turn_summary(c))
sent_at = now()
r = ao("send", "--session", sid, "--message", QUESTION)
note("send", rc=r.returncode, out=r.stdout[-300:], err=r.stderr[-300:])

# 5. wait for the agent's final reply ("cycle done") after the send
deadline = time.time() + 300
final_at = None
while time.time() < deadline:
    c = conv(sid)
    texts = assistant_texts(c)
    if any("cycle done" in (t or "").lower() for _, t, _ in texts):
        final_at = now()
        note("final_reply_seen", assistant=texts[-3:], turns=turn_summary(c), controller=c.get("controller"))
        break
    time.sleep(1)
if not final_at:
    note("abort", reason="no final reply", assistant=assistant_texts(conv(sid))[-3:])
    sys.exit(4)

# 6. observe the user's turn after the reply
marks = [5, 30, 90, 180, 300, 600, 900, 1200]
start = time.time()
shots = {30: "after-30s", 180: "after-3m"}
for m in [x for x in marks if x <= observe]:
    time.sleep(max(0, start + m - time.time()))
    c = conv(sid)
    note("observe", seconds_after_final=m, controller=c.get("controller"), session=session_state(sid),
         turns=turn_summary(c))
    if m in shots:
        shot(sid, shots[m])

json.dump(conv(sid), open(f"{ev}/{lab}-conversation.json", "w"), indent=1)
note("done", session=sid)
